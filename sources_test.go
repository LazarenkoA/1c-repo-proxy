package main

import (
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"os"
	"strings"
	"testing"
)

func TestExtractCommitSourcesRealDump(t *testing.T) {
	msg, err := os.ReadFile("testdata/conn1-010-DevDepot_commitObjects.xml")
	if err != nil {
		t.Skipf("дамп коммита недоступен: %v", err)
	}

	blocks := extractCommitSources(msg)
	require.NotEmpty(t, blocks)

	found := false
	for i, b := range blocks {
		has := strings.Contains(b, "12323232323")
		if has {
			found = true
		}
		t.Logf("блок %d: %d символов, маркер=%v, начало: %q", i+1, len(b), has, firstN(b, 60))
	}

	assert.True(t, found)
}

// Коммит с изменением модуля формы: модуль вшит в описание формы строковым литералом.
func TestExtractCommitSourcesFormDump(t *testing.T) {
	msg, err := os.ReadFile("testdata/conn1-061-DevDepot_commitObjects.xml")
	if err != nil {
		t.Skipf("дамп коммита формы недоступен: %v", err)
	}

	blocks := extractCommitSources(msg)
	require.NotEmpty(t, blocks)

	found := false
	for i, b := range blocks {
		has := strings.Contains(b, "ДобавитьКатегорию")
		if has {
			found = true
		}
		t.Logf("блок %d: %d символов, форма=%v, начало: %q", i+1, len(b), has, firstN(b, 60))
	}

	assert.True(t, found, "модуль формы (Процедура ДобавитьКатегорию) не найден")
}

// Коммит с изменением метаданных: у справочника "Картинки" добавлен реквизит Реквизит2.
func TestExtractCommitSourcesMetaDump(t *testing.T) {
	msg, err := os.ReadFile("testdata/conn1-027-DevDepot_commitObjects.xml")
	if err != nil {
		t.Skipf("дамп коммита метаданных недоступен: %v", err)
	}

	blocks := extractCommitSources(msg)
	require.NotEmpty(t, blocks)

	found := false
	for i, b := range blocks {
		has := strings.Contains(b, "Реквизит2")
		if has {
			found = true
		}
		t.Logf("блок %d: %d символов, метаданные=%v, начало: %q", i+1, len(b), has, firstN(b, 80))
	}

	assert.True(t, found, "реквизит Реквизит2 не найден в извлечённых данных")
}

func firstN(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return s[:n]
	}
	return s
}
