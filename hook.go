package main

import (
	"log"
	"os"
	"os/exec"
	"strings"
)

// SetCommitHook задаёт команду, запускаемую после успешной пересылки
// коммита в хранилище (--on-commit / --on-commit-dir).
func (p *Proxy) SetCommitHook(cmd, dir string) {
	p.onCommit = cmd
	p.onCommitDir = dir
}

// runCommitHook запускает пользовательскую команду асинхронно.
// Команде передаются переменные окружения:
//
//	CRS_REPO    — имя хранилища (alias из строки подключения конфигуратора)
//	CRS_USER    — пользователь хранилища
//	CRS_COMMENT — комментарий коммита
//	CRS_CALL    — имя вызова (DevDepot_commitObjects / DevDepot_changeVersion)
func (p *Proxy) runCommitHook(msg []byte) {
	if p.onCommit == "" {
		return
	}

	doc, err := xmlqueryParse(msg)
	if err != nil {
		return
	}

	call := callName(msg)
	user := xmlAttrValue(doc, "//crs:auth", "user")
	alias := xmlAttrValue(doc, "//crs:call", "alias")
	comment := ""
	if n := xmlqueryQuery(doc, `//crs:comment/text()`); n != nil {
		comment = strings.TrimSpace(n.Data)
	}

	cmd := exec.Command("cmd", "/c", p.onCommit)
	if p.onCommitDir != "" {
		cmd.Dir = p.onCommitDir
	}
	cmd.Env = append(os.Environ(),
		"CRS_REPO="+alias,
		"CRS_USER="+user,
		"CRS_COMMENT="+comment,
		"CRS_CALL="+call,
	)

	log.Printf("hook: running on-commit command (repo=%s user=%s call=%s comment=%q)",
		alias, user, call, comment)
	if err := cmd.Start(); err != nil {
		log.Printf("hook: failed to start command: %v", err)
		return
	}

	go func() {
		if err := cmd.Wait(); err != nil {
			log.Printf("hook: command exited with error: %v", err)
		} else {
			log.Printf("hook: command finished ok")
		}
	}()
}
