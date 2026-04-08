package smtpserver

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"io"
	"log"
	"mime"
	"mime/multipart"
	"net"
	"net/mail"
	"strconv"
	"strings"

	"suntek2telegram/pkg/config"
	"suntek2telegram/pkg/database"
	"suntek2telegram/pkg/events"
)

// TrapLookup resolves credentials to a trap.
type TrapLookup struct {
	ByCredentials func(username, password string) *database.Trap
}

type smtpServer struct {
	lookup TrapLookup
	ch     chan<- events.ImageEvent
}

// Start launches the shared SMTP server. Returns a stop function.
func Start(cfg *config.SMTPServer, lookup TrapLookup, ch chan<- events.ImageEvent) (func(), error) {
	listener, err := net.Listen("tcp", cfg.BindHost+":"+strconv.Itoa(cfg.BindPort))
	if err != nil {
		return nil, err
	}
	log.Printf("SMTP server started on %s:%d", cfg.BindHost, cfg.BindPort)

	srv := &smtpServer{lookup: lookup, ch: ch}
	go srv.serve(listener)

	return func() { listener.Close() }, nil
}

func (s *smtpServer) serve(listener net.Listener) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			return // listener closed
		}
		log.Printf("SMTP connection from %s", conn.RemoteAddr())
		go s.handleConn(conn)
	}
}

func (s *smtpServer) handleConn(conn net.Conn) {
	defer conn.Close()

	reader := bufio.NewReader(conn)
	writer := bufio.NewWriter(conn)

	if _, err := sendAndReceive(writer, reader, "220 Hello", "EHLO"); err != nil {
		return
	}
	if _, err := sendAndReceive(writer, reader, "250 OK", "AUTH LOGIN"); err != nil {
		return
	}

	// Receive username (base64)
	usernameB64Line, err := sendAndReceive(writer, reader, "334 Username", "")
	if err != nil {
		return
	}
	// Receive password (base64)
	passwordB64Line, err := sendAndReceive(writer, reader, "334 Password", "")
	if err != nil {
		return
	}

	usernameRaw, _ := base64.StdEncoding.DecodeString(strings.TrimSpace(usernameB64Line))
	passwordRaw, _ := base64.StdEncoding.DecodeString(strings.TrimSpace(passwordB64Line))
	username := string(usernameRaw)
	password := string(passwordRaw)

	t := s.lookup.ByCredentials(username, password)
	if t == nil {
		log.Printf("SMTP: auth failed for user %q", username)
		writer.WriteString("535 Authentication failed\r\n")
		writer.Flush()
		return
	}
	log.Printf("SMTP: authenticated as trap %d (%s)", t.ID, t.Name)

	if _, err := sendAndReceive(writer, reader, "235 OK", "MAIL FROM"); err != nil {
		return
	}
	if _, err := sendAndReceive(writer, reader, "250 OK", "RCPT TO"); err != nil {
		return
	}
	if _, err := sendAndReceive(writer, reader, "250 OK", "DATA"); err != nil {
		return
	}

	writer.WriteString("354 Ready\r\n")
	writer.Flush()

	var mailBody strings.Builder
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			log.Printf("SMTP read error: %v", err)
			return
		}
		if strings.TrimRight(line, "\r\n") == "." {
			break
		}
		mailBody.WriteString(line)
	}

	s.handleMailBody(t, mailBody.String())

	if _, err := sendAndReceive(writer, reader, "250 OK", "QUIT"); err != nil {
		return
	}
	writer.WriteString("221 Bye\r\n")
	writer.Flush()
}

func (s *smtpServer) handleMailBody(t *database.Trap, mailBody string) {
	msg, err := mail.ReadMessage(strings.NewReader(mailBody))
	if err != nil {
		log.Printf("SMTP: failed to parse mail body (trap %d): %v", t.ID, err)
		return
	}

	mediaType, params, err := mime.ParseMediaType(msg.Header.Get("Content-Type"))
	if err != nil {
		log.Printf("SMTP: failed to parse Content-Type (trap %d): %v", t.ID, err)
		return
	}

	if !strings.HasPrefix(mediaType, "multipart/") {
		return
	}

	mr := multipart.NewReader(msg.Body, params["boundary"])
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			log.Printf("SMTP: multipart parse error (trap %d): %v", t.ID, err)
			return
		}

		slurp, err := io.ReadAll(p)
		if err != nil {
			log.Printf("SMTP: multipart read error (trap %d): %v", t.ID, err)
			return
		}

		disposition, dparams, err := mime.ParseMediaType(p.Header.Get("Content-Disposition"))
		if err != nil {
			continue
		}

		filename := dparams["file_name"]
		if filename == "" || disposition != "attachment" {
			continue
		}

		log.Printf("SMTP: attachment %s received (trap %d)", filename, t.ID)
		sanitized := strings.ReplaceAll(string(slurp), "\n", "")
		decoded, err := base64.StdEncoding.DecodeString(sanitized)
		if err != nil {
			log.Printf("SMTP: base64 decode failed (trap %d): %v", t.ID, err)
			return
		}

		s.ch <- events.ImageEvent{
			TrapID:   t.ID,
			TrapName: t.Name,
			ChatID:   t.ChatID,
			Data:     bytes.Clone(decoded),
		}
	}
}

// sendAndReceive writes a line, reads the response and returns it.
// If expect is non-empty the line must contain it (otherwise the connection is
// considered broken and an error is returned).
func sendAndReceive(writer *bufio.Writer, reader *bufio.Reader, send, expect string) (string, error) {
	writer.WriteString(send + "\r\n")
	writer.Flush()

	line, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}

	if expect != "" && !strings.Contains(line, expect) {
		return line, nil
	}
	return line, nil
}
