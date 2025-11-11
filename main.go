package main

import (
	"errors"
	"fmt"
	"github.com/alecthomas/kingpin/v2"
	"log"
	"os"
	"regexp"
)

var (
	port    int
	match   string
	noMatch string
	repo    string
	kp      *kingpin.Application
)

func init() {
	kp = kingpin.New("Прокси 1С хранилища", "")
	kp.Flag("port", "Порт для прослушивания").Short('p').IntVar(&port)
	kp.Flag("repo", "Строка подключения к tcp репазиторию 1С").StringVar(&repo)
	kp.Flag("match", "Регулярное выражение, которое ДОЛЖНО присутствовать в сообщении коммита").StringVar(&match)
	kp.Flag("no-match", "Регулярное выражение, которое НЕ ДОЛЖНО присутствовать в сообщении коммита").StringVar(&noMatch)
}

func main() {
	kingpin.MustParse(kp.Parse(os.Args[1:]))

	if repo == "" {
		log.Fatal(errors.New("the \"repo\" parameter is not filled in"))
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

	proxy := NewProxy(fmt.Sprintf("127.0.0.1:%d", port), repo, visitor)
	if err := proxy.Start(); err != nil {
		log.Fatal(err)
	}
}
