package main

import (
	"bytes"
	"fmt"
	"github.com/antchfx/xmlquery"
	"github.com/antchfx/xpath"
	"io"
	"log"
	"net"
	"sync"
	"time"
)

type Proxy struct {
	localAddr   string
	remoteAddr  string
	listener    net.Listener
	connections sync.Map
	connID      int64
	visitor     func(message string) error
}

const (
	nameSpace = "http://v8.1c.ru/8.2/crs"
)

var (
	end = []byte{0x66, 0x53, 0xb2, 0xa6}
)

func NewProxy(localAddr, remoteAddr string, visitor func(message string) error) *Proxy {

	return &Proxy{
		localAddr:  localAddr,
		remoteAddr: remoteAddr,
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

	// Запускаем двунаправленное копирование данных с логированием
	p.copy(clientConn, serverConn)
}

func (p *Proxy) copy(client, server net.Conn) {
	var wg sync.WaitGroup
	wg.Add(2)

	r, w := io.Pipe()

	// Копируем данные от клиента к серверу (upstream)
	go func() {
		defer server.(*net.TCPConn).CloseWrite()
		defer wg.Done()
		defer w.Close()

		for {
			msg, _ := readUntil(client, end)
			if err := p.check(msg); err == nil {
				_, _ = server.Write(msg)
				_, _ = server.Write(end)
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

func readUntil(r io.Reader, delim []byte) ([]byte, error) {
	buf := make([]byte, 0, 4096)
	chunk := make([]byte, 1024)

	for {
		n, err := r.Read(chunk)
		if n > 0 {
			buf = append(buf, chunk[:n]...)
			if idx := bytes.Index(buf, delim); idx != -1 {
				return buf[:idx], nil
			}
		}

		if err != nil {
			return buf, err
		}
	}
}
