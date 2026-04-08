package ftpserver

import (
	"bytes"
	"io"
	"log"
	"sync"

	"goftp.io/server/v2"
	"suntek2telegram/pkg/config"
	"suntek2telegram/pkg/database"
	"suntek2telegram/pkg/events"
)

// TrapLookup is provided by the manager to resolve credentials to a trap.
type TrapLookup struct {
	ByCredentials func(username, password string) *database.Trap
}

type singleAuth struct {
	lookup   TrapLookup
	sessions sync.Map // *server.Session → *database.Trap
}

type singlePerm struct{}

type singleDriver struct {
	sessions *sync.Map
	ch       chan<- events.ImageEvent
}

// Start launches the shared FTP server. Returns a stop function.
func Start(cfg *config.FTPServer, lookup TrapLookup, ch chan<- events.ImageEvent) (func(), error) {
	auth := &singleAuth{lookup: lookup}
	driver := &singleDriver{sessions: &auth.sessions, ch: ch}

	opts := &server.Options{
		Driver:       driver,
		Perm:         &singlePerm{},
		Auth:         auth,
		Port:         cfg.BindPort,
		Hostname:     cfg.BindHost,
		PassivePorts: cfg.PassivePorts,
		PublicIP:     cfg.PublicIP,
	}

	srv, err := server.NewServer(opts)
	if err != nil {
		return nil, err
	}

	go func() {
		if err := srv.ListenAndServe(); err != nil {
			log.Printf("FTP server stopped: %v", err)
		}
	}()

	log.Printf("FTP server started on %s:%d", cfg.BindHost, cfg.BindPort)
	return func() { srv.Shutdown() }, nil
}

func (a *singleAuth) CheckPasswd(ctx *server.Context, username, password string) (bool, error) {
	t := a.lookup.ByCredentials(username, password)
	if t == nil {
		return false, nil
	}
	a.sessions.Store(ctx.Sess, t)
	return true, nil
}

func (d *singleDriver) PutFile(ctx *server.Context, destPath string, data io.Reader, appendData int64) (int64, error) {
	val, ok := d.sessions.Load(ctx.Sess)
	if !ok {
		return 0, nil
	}
	t := val.(*database.Trap)

	var buf bytes.Buffer
	copied, err := io.Copy(&buf, data)
	if err != nil {
		return copied, err
	}

	d.ch <- events.ImageEvent{
		TrapID:   t.ID,
		TrapName: t.Name,
		ChatID:   t.ChatID,
		Data:     buf.Bytes(),
	}

	// Close after each image — camera firmware has no keep-alive through mobile NAT.
	d.sessions.Delete(ctx.Sess)
	ctx.Sess.Close()
	return copied, nil
}
