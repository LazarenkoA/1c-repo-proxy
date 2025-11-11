package main

import (
	"bytes"
	"fmt"
)

func createCrServerExceptionXml(message string) []byte {
	exp := []byte(fmt.Sprintf(`<exception  xmlns="http://v8.1c.ru/8.2/virtual-resource-system" xmlns:xs="http://www.w3.org/2001/XMLSchema" 
xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance"   
xsi:type="Exception" 
clsid="580392e6-ba49-4280-ac67-fcd6f2180121"   
reason="406">    
<descr xmlns="http://v8.1c.ru/8.1/data/core">%s</descr></exception>`, message))

	req := bytes.Buffer{}
	req.WriteString("HTTP/1.1 500 OK\r\n")
	req.WriteString("Content-Type: application/xml; charset=utf-8\r\n")
	req.WriteString(fmt.Sprintf("Content-Length: %d\r\n", len(exp)))
	req.WriteString("\r\n")
	req.Write(exp)

	return req.Bytes()
}
