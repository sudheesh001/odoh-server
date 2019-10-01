package main

import (
	"encoding/base64"
	"io/ioutil"
	"fmt"
	"log"
	"time"
	"net"
	"net/http"
	"github.com/miekg/dns"
)

type odohServer struct {
	verbose  bool
	nameserver string
	timeout time.Duration
	connection *dns.Conn
}

func (s *odohServer) startConnection(nameserver string, timeout time.Duration) error {
	s.connection = new(dns.Conn)
	var err error
	if s.connection.Conn, err = net.DialTimeout("tcp", nameserver, timeout * time.Millisecond); err != nil {
		return fmt.Errorf("Failed starting resolver connection")
	}

	return nil
}

func (s *odohServer) resolve(msg *dns.Msg) (*dns.Msg, error) {
	err := s.startConnection(s.nameserver, s.timeout)
	if err != nil {
		return nil, err
	}

	s.connection.SetReadDeadline(time.Now().Add(s.timeout * time.Millisecond))
	s.connection.SetWriteDeadline(time.Now().Add(s.timeout * time.Millisecond))

	if err := s.connection.WriteMsg(msg); err != nil {
		return nil, err
	}

	response, err := s.connection.ReadMsg()
	if err != nil {
		return nil, err
	}

	return response, nil
}

func (s *odohServer) parseRequestFromGET(r *http.Request) (string, string, uint16, error) {
	encoded := r.URL.Query().Get("dns")
	if encoded == "" {
		return "", "", uint16(0), fmt.Errorf("Missing DNS query parameter in GET request")
	}

	decoded, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return "", "", uint16(0), err
	}

	msg := &dns.Msg{}
	if err := msg.Unpack(decoded); err != nil {
		return "", "", uint16(0), err
	}
	if len(msg.Question) != 1 {
		return "", "", uint16(0), err
	}
	
	return msg.Question[0].Name, dns.Type(msg.Question[0].Qtype).String(), msg.Id, nil
}

func (s *odohServer) parseRequestFromPOST(r *http.Request) (string, string, uint16, error) {
	if r.Header.Get("Content-Type") != "application/dns-message" {
		return "", "", uint16(0), fmt.Errorf("incorrect content type, expected 'application/dns-message', got %s", r.Header.Get("Content-Type"))
	}
	defer r.Body.Close()
	body, err := ioutil.ReadAll(r.Body)
	if err != nil {
		return "", "", uint16(0), err
	}

	// Parse the DNS message
	msg := &dns.Msg{}
	if err := msg.Unpack(body); err != nil {
		return "", "", uint16(0), err
	}
	if len(msg.Question) != 1 {
		return "", "", uint16(0), err
	}

	if s.verbose {
		log.Printf("%s Unpacked DNS message:\n %s\n", r.Method, msg)
	}

	return msg.Question[0].Name, dns.Type(msg.Question[0].Qtype).String(), msg.Id, nil
}

func (s *odohServer) parseRequest(r *http.Request) (string, string, uint16, error) {
	switch r.Method {
	case "GET":
		return s.parseRequestFromGET(r)
	case "POST":
		return s.parseRequestFromPOST(r)
	default:
		return "", "", uint16(0), fmt.Errorf("unsupported HTTP method")
	}
}

func createQuery(n, t string) *dns.Msg {
	queryMessage := &dns.Msg {
		MsgHdr: dns.MsgHdr {
			Opcode: dns.OpcodeQuery,
		},
		Question: make([]dns.Question, 1),
	}

	qtype := dns.TypeAAAA
	if t == "A" {
		qtype = dns.TypeA
	}

	queryMessage.Question[0] = dns.Question{
		Name: dns.Fqdn(n), 
		Qtype: qtype,
		Qclass: uint16(dns.ClassINET),
	}
	queryMessage.Id = dns.Id()
	queryMessage.Rcode = dns.RcodeSuccess
	queryMessage.RecursionDesired = true

	return queryMessage
}

func (s *odohServer) queryHandler(w http.ResponseWriter, r *http.Request) {
	log.Println("Handling /odoh request")

	n, t, id, err := s.parseRequest(r)
	if err != nil {
		log.Println("Failed parsing request:", err)
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
			
	if s.verbose {
		log.Printf("%s Resolving: %s %s %d", r.Method, n, t, id)
	}

	query := createQuery(n, t)
	start := time.Now()
	response, err := s.resolve(query)
	elapsed := time.Now().Sub(start)
	if err != nil {
		log.Println("Query failed:", err)
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	packed, err := response.Pack()
	if err != nil {
		log.Println("Failed packing answers:", err)
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	if s.verbose {
		log.Printf("%s Query: qname='%s' qtype='%s' qid=%d elapsed=%s\n", r.Method, n, t, id, elapsed.String())
		log.Printf("%s Answer: ", r.Method, response.Answer)
		log.Printf("%s Full response: %s\n", r.Method, string(packed))
		log.Printf("%s Raw response: %x\n", r.Method, packed)
	}

	w.Header().Set("Content-Type", "application/dns-message")
	w.Write(packed)
}

func handle(w http.ResponseWriter, r *http.Request) {
	log.Println("Received / request")
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	fmt.Fprint(w, "ODOH, try /dns-query instead!")
}

func healthCheckHandler(w http.ResponseWriter, r *http.Request) {
	log.Println("Received /health request")
	fmt.Fprint(w, "ok")
}

func main() {
	timeout := 2500 * time.Millisecond
	server := odohServer {
		verbose: true,
		timeout: timeout,
		nameserver: "1.1.1.1:53",
	}

	http.HandleFunc("/dns-query", server.queryHandler)
	http.HandleFunc("/health", healthCheckHandler)
	http.HandleFunc("/", handle)

	log.Print("Listening on port 8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}
