package main

import (
	"bytes"
	"fmt"
	"github.com/antchfx/xmlquery"
	"github.com/antchfx/xpath"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"sync/atomic"
	"time"
)

type Proxy struct {
	localAddr   string
	remoteAddr  string
	dumpDir     string
	onCommit    string
	onCommitDir string
	listener    net.Listener
	connections sync.Map
	connID      int64
	visitor     func(message string) error
}

const (
	nameSpace = "http://v8.1c.ru/8.2/crs"
)

var (
	end         = []byte{0x66, 0x53, 0xb2, 0xa6}
	commitCalls = map[string]struct{}{
		"DevDepot_commitObjects": {},
		"DevDepot_changeVersion": {},
	}
)

func NewProxy(localAddr, remoteAddr, dumpDir string, visitor func(message string) error) *Proxy {

	return &Proxy{
		localAddr:  localAddr,
		remoteAddr: remoteAddr,
		dumpDir:    dumpDir,
		visitor:    visitor,
	}
}

func (p *Proxy) Start() error {
	listener, err := net.Listen("tcp", p.localAddr)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %v", p.localAddr, err)
	}

	p.listener = listener
	log.Printf("Enhanced TCP Proxy started: %s -> %s", p.localAddr, p.remoteAddr)

	for {
		clientConn, err := listener.Accept()
		if err != nil {
			log.Printf("Failed to accept connection: %v", err)
			continue
		}

		// Подключаемся к целевому серверу
		go p.serverConnect(clientConn)
	}
}

func (p *Proxy) serverConnect(clientConn net.Conn) {
	serverConn, err := net.DialTimeout("tcp", p.remoteAddr, 10*time.Second)
	if err != nil {
		log.Printf("Failed to connect to server %s: %v", p.remoteAddr, err)
		return
	}
	defer serverConn.Close()
	defer clientConn.Close()

	connID := atomic.AddInt64(&p.connID, 1)
	log.Printf("conn=%d new connection %s -> %s", connID, clientConn.RemoteAddr(), p.remoteAddr)

	// Запускаем двунаправленное копирование данных с логированием
	p.copy(connID, clientConn, serverConn)
}

func (p *Proxy) copy(connID int64, client, server net.Conn) {
	var wg sync.WaitGroup
	wg.Add(2)

	r, w := io.Pipe()

	// Копируем данные от клиента к серверу (upstream)
	go func() {
		defer server.(*net.TCPConn).CloseWrite()
		defer wg.Done()
		defer w.Close()

		seq := 0
		reader := &delimReader{conn: client, delim: end}
		for {
			msg, complete := reader.next()
			if len(msg) > 0 {
				p.dumpPacket(connID, seq, msg)
				seq++
			}
			if !complete {
				break
			}
			if err := p.check(msg); err == nil {
				_, _ = server.Write(msg)
				_, _ = server.Write(end)
				if _, ok := commitCalls[callName(msg)]; ok {
					go p.runCommitHook(msg)
				}
			} else {
				exp := createCrServerExceptionXml("Коммит не соответствует правилам проверки")
				_, err = w.Write(exp)
				_, _ = w.Write(end)
			}
		}
	}()
	// Копируем данные от сервера к клиенту (downstream)
	go func() {
		defer wg.Done()
		defer client.(*net.TCPConn).CloseWrite()

		r2, w2 := io.Pipe()

		// копируем данные сервера → w2
		go func() {
			_, err := io.Copy(w2, server)
			w2.CloseWithError(err)
		}()

		// копируем данные из r (ошибки из upstream) → w2
		go func() {
			_, err := io.Copy(w2, r)
			w2.CloseWithError(err)
		}()

		// читаем объединённый поток → клиент
		_, _ = io.Copy(client, r2)
	}()

	wg.Wait()
}

func (p *Proxy) check(line []byte) error {
	doc, err := xmlquery.Parse(bytes.NewReader(line))
	if err != nil {
		return nil
	}

	ns := map[string]string{
		"crs": nameSpace,
	}
	commitObjects, _ := xpath.CompileWithNS(`//crs:call[@name="DevDepot_commitObjects"]`, ns)
	node := xmlquery.QuerySelector(doc, commitObjects)
	if node == nil {
		return nil
	}

	commitObjects, _ = xpath.CompileWithNS(`//crs:comment/text()`, ns)
	node = xmlquery.QuerySelector(node, commitObjects)
	return p.visitor(Opt(node).Data)
}

func callName(msg []byte) string {
	if m := callNameRe.FindSubmatch(msg); m != nil {
		return string(m[1])
	}
	return "unknown"
}

func (p *Proxy) dumpPacket(connID int64, seq int, msg []byte) {
	if p.dumpDir == "" {
		return
	}

	call := callName(msg)

	fn := filepath.Join(p.dumpDir, fmt.Sprintf("conn%d-%03d-%s.xml", connID, seq, call))
	if err := os.WriteFile(fn, msg, 0644); err != nil {
		log.Printf("conn=%d seq=%d dump failed: %v", connID, seq, err)
		return
	}
	log.Printf("conn=%d seq=%d call=%s size=%d dumped to %s", connID, seq, call, len(msg), fn)

	if _, ok := commitCalls[call]; ok {
		p.dumpSources(connID, seq, call, msg)
	}
}

var callNameRe = regexp.MustCompile(`<crs:call[^>]*\bname="([^"]+)"`)

// delimReader читает сообщения, разделённые маркером end, сохраняя
// остаток буфера между чтениями (сообщения могут слипаться в одном TCP-чанке).
type delimReader struct {
	conn  io.Reader
	delim []byte
	buf   []byte
	chunk [4096]byte
}

func (d *delimReader) next() (msg []byte, complete bool) {
	for {
		if idx := bytes.Index(d.buf, d.delim); idx != -1 {
			msg = append(msg, d.buf[:idx]...)
			copy(d.buf, d.buf[idx+len(d.delim):])
			d.buf = d.buf[:len(d.buf)-idx-len(d.delim)]
			return msg, true
		}

		n, err := d.conn.Read(d.chunk[:])
		if n > 0 {
			d.buf = append(d.buf, d.chunk[:n]...)
		}
		if err != nil {
			// Хвост без разделителя отдаём как есть (незавершённое сообщение)
			if len(d.buf) > 0 {
				msg = append(msg, d.buf...)
				d.buf = d.buf[:0]
			}
			return msg, false
		}
	}
}
