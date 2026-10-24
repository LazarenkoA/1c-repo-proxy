package main

import (
	"errors"
	"fmt"
	"github.com/alecthomas/kingpin/v2"
	"log"
	"net/url"
	"os"
	"regexp"
)

var (
	port       int
	match      string
	noMatch    string
	repo       string
	dump       string
	onCommit   string
	onCommitWD string
	kp         *kingpin.Application
)

func init() {
	// --port 1543 --repo tcp://localhost:1542/test

	kp = kingpin.New("Прокси 1С хранилища", "")
	kp.Flag("port", "Порт для прослушивания").Short('p').IntVar(&port)
	kp.Flag("repo", "Строка подключения к tcp репазиторию 1С").StringVar(&repo)
	kp.Flag("match", "Регулярное выражение, которое ДОЛЖНО присутствовать в сообщении коммита").StringVar(&match)
	kp.Flag("no-match", "Регулярное выражение, которое НЕ ДОЛЖНО присутствовать в сообщении коммита").StringVar(&noMatch)
	kp.Flag("dump", "Каталог для дампа всех пакетов от конфигуратора (для анализа протокола)").StringVar(&dump)
	kp.Flag("on-commit", "Команда, запускаемая после успешного помещения в хранилище (переменные окружения CRS_REPO, CRS_USER, CRS_COMMENT, CRS_CALL)").StringVar(&onCommit)
	kp.Flag("on-commit-dir", "Рабочий каталог для команды --on-commit").StringVar(&onCommitWD)
}

func main() {
	kingpin.MustParse(kp.Parse(os.Args[1:]))

	if repo == "" {
		log.Fatal(errors.New("the \"repo\" parameter is not filled in"))
	}

	if dump != "" {
		if err := os.MkdirAll(dump, 0755); err != nil {
			log.Fatalf("failed to create dump dir %s: %v", dump, err)
		}
		log.Printf("packet dump enabled: %s", dump)
	}

	visitor := func(message string) error {
		if match != "" {
			var re = regexp.MustCompile("(?m)" + match)
			if !re.MatchString(message) {
				return errors.New("the message does not contain the required regexp template")
			}
		}
		if noMatch != "" {
			var re = regexp.MustCompile("(?m)" + noMatch)
			if re.MatchString(message) {
				return errors.New("the message contains a forbidden regexp pattern")
			}
		}

		return nil
	}

	// Строка подключения вида tcp://host:port/имя_хранилища — для dial нужен только host:port
	remote := repo
	if u, err := url.Parse(repo); err == nil && u.Host != "" {
		remote = u.Host
	}

	proxy := NewProxy(fmt.Sprintf("127.0.0.1:%d", port), remote, dump, visitor)
	proxy.SetCommitHook(onCommit, onCommitWD)
	if err := proxy.Start(); err != nil {
		log.Fatal(err)
	}
}
