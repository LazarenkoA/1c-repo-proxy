package main

import (
	"bytes"
	"compress/flate"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"github.com/antchfx/xmlquery"
	"github.com/antchfx/xpath"
	"github.com/v8platform/brackets"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"
)

// Формат <crs:data> в DevDepot_commitObjects / DevDepot_changeVersion (установлен эмпирически):
//   base64( 02 01 | GUID(16 байт) | raw-deflate(контейнер) )
// Контейнер — цепочка записей [uint64 len LE][данные]:
//   - данные могут быть текстом 1С-сериализации (BOM или '{' в начале)
//   - либо raw-deflate потоком (вложенные уровни)
//   - либо двоичным модулем: блоки вида "\r\nXXXXXXXX XXXXXXXX 7fffffff \r\n" + BOM + исходник
//     (первое число — длина данных блока в байтах)

var moduleBlockRe = regexp.MustCompile(`\r\n([0-9a-f]{8}) [0-9a-f]{8} 7fffffff \r\n`)

var bom = []byte{0xEF, 0xBB, 0xBF}

var queryCache sync.Map // expr -> *xpath.Expr

func xmlqueryParse(msg []byte) (*xmlquery.Node, error) {
	return xmlquery.Parse(bytes.NewReader(msg))
}

func xmlqueryQuery(doc *xmlquery.Node, expr string) *xmlquery.Node {
	v, _ := queryCache.LoadOrStore(expr, func() any {
		e, _ := xpath.CompileWithNS(expr, map[string]string{"crs": nameSpace})
		return e
	}())
	e, _ := v.(*xpath.Expr)
	if e == nil {
		return nil
	}
	return xmlquery.QuerySelector(doc, e)
}

// xmlAttrValue возвращает значение атрибута attr у ноды, найденной по expr
func xmlAttrValue(doc *xmlquery.Node, expr, attr string) string {
	node := xmlqueryQuery(doc, expr)
	if node == nil {
		return ""
	}
	for _, a := range node.Attr {
		if a.Name.Local == attr {
			return a.Value
		}
	}
	return ""
}

// extractCommitSources извлекает из пакета коммита все текстовые блоки (исходники модулей
// и текстовые метаданные). Возвращает nil, если пакет не содержит <crs:data>.
func extractCommitSources(msg []byte) []string {
	doc, err := xmlqueryParse(msg)
	if err != nil {
		return nil
	}
	node := xmlqueryQuery(doc, `//crs:data/text()`)
	if node == nil {
		return nil
	}

	b64 := strings.Map(func(r rune) rune {
		if r == '\r' || r == '\n' || r == ' ' || r == '\t' {
			return -1
		}
		return r
	}, node.Data)

	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || len(raw) <= 18 || raw[0] != 0x02 || raw[1] != 0x01 {
		return nil
	}

	container := tryFlate(raw[18:])
	if container == nil {
		return nil
	}

	seen := map[string]bool{}
	var out []string
	harvestTexts(container, &out, seen, 0)
	return out
}

// harvestTexts рекурсивно обходит контейнер записей и собирает текстовые блоки.
func harvestTexts(data []byte, out *[]string, seen map[string]bool, depth int) {
	if depth > 8 {
		return
	}

	trimmed := bytes.TrimPrefix(data, bom)
	if utf8.Valid(trimmed) {
		extractFromBracketText(string(trimmed), out, seen)
		return
	}

	// Двоичный модуль: блоки "\r\nXXXXXXXX XXXXXXXX 7fffffff \r\n" + BOM + текст
	for _, block := range extractModuleBlocks(data) {
		addBlock(out, seen, block)
	}

	for pos := 0; pos < len(data); {
		if pos+8 > len(data) {
			break
		}
		ln := binary.LittleEndian.Uint64(data[pos:])
		if ln == 0 || ln > uint64(len(data)-pos-8) {
			pos++
			continue
		}
		payloadStart := pos + 8
		payload := data[payloadStart : payloadStart+int(ln)]

		// Расширенная запись: заголовок [магия 44 33 22 11][uint32][uint32][uint64 длина]
		// + данные; само поле ln при этом мусорное
		if len(payload) >= 20 && bytes.Equal(payload[:4], extMagic) {
			datalen := binary.LittleEndian.Uint64(payload[12:20])
			start := payloadStart + 20
			if datalen > 0 && start+int(datalen) <= len(data) {
				if text, ok := asText(data[start : start+int(datalen)]); ok {
					addBlock(out, seen, text)
				}
				pos = start + int(datalen)
				continue
			}
			pos++
			continue
		}

		if text, ok := asText(payload); ok {
			addBlock(out, seen, text)
		} else if dec := tryFlate(payload); dec != nil {
			harvestTexts(dec, out, seen, depth+1)
		}

		pos = payloadStart + int(ln)
	}
}

var extMagic = []byte{0x44, 0x33, 0x22, 0x11}

// extractModuleBlocks достаёт текстовые блоки из двоичного модуля
// (формат "XXXXXXXX XXXXXXXX 7fffffff" + BOM + текст длиной из первого числа).
func extractModuleBlocks(data []byte) []string {
	var out []string
	for _, loc := range moduleBlockRe.FindAllSubmatchIndex(data, -1) {
		start := loc[1]
		ln, err := parseHexUint32(data[loc[2]:loc[3]])
		if err != nil || ln <= 0 || start+ln > len(data) {
			continue
		}
		if text, ok := asText(data[start : start+ln]); ok {
			out = append(out, text)
		}
	}
	return out
}

func addBlock(out *[]string, seen map[string]bool, block string) {
	key := strings.TrimSpace(block)
	if key == "" || seen[key] {
		return
	}
	seen[key] = true
	*out = append(*out, block)
}

// asText считает данные текстом, если это валидный UTF-8 (опционально с BOM)
// и он начинается не с служебного '{' и содержит хоть одну букву.
func asText(data []byte) (string, bool) {
	data = bytes.TrimPrefix(data, bom)
	if !utf8.Valid(data) {
		return "", false
	}
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || trimmed[0] == '{' {
		return "", false
	}
	hasLetter := false
	for _, r := range string(trimmed) {
		if unicodeIsLetter(r) {
			hasLetter = true
			break
		}
	}
	if !hasLetter {
		return "", false
	}
	return string(data), true
}

// extractFromBracketText разбирает текст 1С-сериализации (скобкофайл) библиотекой
// github.com/v8platform/brackets и извлекает:
//   - имена элементов метаданных ({1,0,<guid>},"Имя") → сводка "метаданные: Объект [...]"
//   - модули (форм/объектов), вшитые строковыми литералами
func extractFromBracketText(text string, out *[]string, seen map[string]bool) {
	defer func() { _ = recover() }() // битые тексты не должны ронять прокси

	nodes, _ := brackets.NewParser(strings.NewReader(text)).ReadAllNodes()
	if len(nodes) == 0 {
		return
	}

	var metaNames []string

	var walk func(n brackets.Node)
	walk = func(n brackets.Node) {
		var kids []brackets.Node
		for i := 0; ; i++ {
			child, err := n.GetNodeE(i)
			if err != nil {
				break
			}
			kids = append(kids, child)
			walk(child)
		}

		// Строковое значение — кандидат в модуль (парсер сохраняет удвоенные кавычки)
		if txt := n.Get(); txt != "" {
			if unescaped := strings.ReplaceAll(txt, `""`, `"`); looksLikeModule(unescaped) {
				addBlock(out, seen, unescaped)
			}
		}

		// Пара {1,0,<guid>} + сосед-строка = имя элемента метаданных
		for i := 0; i+1 < len(kids); i++ {
			if isMetaRefNode(kids[i]) {
				if name := kids[i+1].Get(); name != "" {
					metaNames = append(metaNames, name)
				}
			}
		}
	}

	for _, n := range nodes {
		walk(n)
	}

	if len(metaNames) >= 3 {
		summary := "метаданные: " + metaNames[0] + " [" + strings.Join(metaNames[1:], ", ") + "]"
		addBlock(out, seen, summary)
	}
}

// isMetaRefNode проверяет ноду вида {1,0,<guid>}
func isMetaRefNode(n brackets.Node) bool {
	if n.Get() != "" { // это значение, а не контейнер
		return false
	}
	if n.Get(0) != "1" || n.Get(1) != "0" {
		return false
	}
	guid := n.Get(2)
	return len(guid) == 36 && strings.Count(guid, "-") == 4
}

func looksLikeModule(s string) bool {
	if len(s) < 40 {
		return false
	}
	if strings.Contains(s, "&НаКлиенте") || strings.Contains(s, "&НаСервере") || strings.Contains(s, "&НаСервер\n") {
		return true
	}
	return (strings.Contains(s, "Процедура ") && strings.Contains(s, "КонецПроцедуры")) ||
		(strings.Contains(s, "Функция ") && strings.Contains(s, "КонецФункции"))
}

func parseHexUint32(b []byte) (int, error) {
	var v int
	for _, c := range b {
		var d int
		switch {
		case c >= '0' && c <= '9':
			d = int(c - '0')
		case c >= 'a' && c <= 'f':
			d = int(c-'a') + 10
		default:
			return 0, io.ErrUnexpectedEOF
		}
		v = v*16 + d
	}
	return v, nil
}

func tryFlate(data []byte) []byte {
	zr := flate.NewReader(bytes.NewReader(data))
	defer zr.Close()

	out, _ := io.ReadAll(zr)
	// допускаем обрезанный поток (io.ErrUnexpectedEOF): большие данные конфигуратор
	// режет на фрагменты-записи, и конец потока может не попадать в одну запись
	if len(out) == 0 {
		return nil
	}
	return out
}

// dumpSources извлекает исходники из пакета коммита и пишет их в файл
// conn<id>-<seq>-<call>.sources.txt рядом с дампом пакета.
func (p *Proxy) dumpSources(connID int64, seq int, call string, msg []byte) {
	blocks := extractCommitSources(msg)
	if len(blocks) == 0 {
		log.Printf("conn=%d seq=%d call=%s no sources extracted", connID, seq, call)
		return
	}

	var sb strings.Builder
	total := 0
	for i, b := range blocks {
		if i > 0 {
			sb.WriteString("\n\n// ============ блок " + itoa(i+1) + " ============\n\n")
		}
		sb.WriteString(b)
		total += len(b)
	}

	fn := filepath.Join(p.dumpDir, fmt.Sprintf("conn%d-%03d-%s.sources.txt", connID, seq, call))
	if err := os.WriteFile(fn, []byte(sb.String()), 0644); err != nil {
		log.Printf("conn=%d seq=%d call=%s sources dump failed: %v", connID, seq, call, err)
		return
	}
	log.Printf("conn=%d seq=%d call=%s extracted %d code block(s), %d chars -> %s",
		connID, seq, call, len(blocks), total, fn)
}

func itoa(v int) string {
	return strconv.Itoa(v)
}

func unicodeIsLetter(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= 'а' && r <= 'я') || (r >= 'А' && r <= 'Я') || (r >= 0x400 && r <= 0x4FF) || r == 'ё' || r == 'Ё' || (r >= 0x100 && r <= 0x24F)
}
