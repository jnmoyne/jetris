//go:build ignore

// gen-qr turns a NATS server's WebSocket URL into a "join this server" link
// for the browser build's join page (web/join.html) and prints that link as a
// QR code: on the terminal, so a phone can scan it straight off the screen,
// and as a PNG to project, print or paste into a chat.
//
// Whoever scans it lands on a page that asks for one thing — their player
// name — and then drops them in that server's lobby; they never see the
// server browser. That is the LAN-party flow: put the QR on the big screen,
// everyone scans it, everyone is in the same lobby. (The desktop build's LAN
// party mode does all of this itself — it serves the browser build and shows
// the code in its lobby; this script is for a server of your own, or a page
// hosted elsewhere.)
//
// Run it from the repo root:
//
//	go run scripts/gen-qr.go wss://demo.nats.io:8443
//	go run scripts/gen-qr.go -name "LAN party" -page http://192.168.1.20:8080/ ws://192.168.1.20:4223
//	go run scripts/gen-qr.go                      # asks for the URL and the name
//
// -page is where the browser build is served from: the GitHub Pages copy by
// default, or your own `python3 -m http.server -d dist/web 8080` (phones must
// be able to reach that page AND the NATS server).
//
// The link is internal/webdist.JoinLink's and the code internal/qr's — the
// same two the game uses — so the script stays a `go run` away with no
// dependency added to the module for it.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"image/png"
	"log"
	"net/url"
	"os"
	"strings"

	"jetris/internal/qr"
	"jetris/internal/webdist"
)

const defaultPage = webdist.DefaultPage

func main() {
	log.SetFlags(0)
	var (
		name   = flag.String("name", "", "name for the server, shown on the join page (optional)")
		page   = flag.String("page", defaultPage, "where the browser build is served from")
		out    = flag.String("out", "jetris-join.png", "PNG to write (empty writes none)")
		scale  = flag.Int("scale", 8, "PNG pixels per QR module")
		border = flag.Int("border", qr.QuietZone, "quiet zone around the code, in modules (4 is the standard minimum)")
		quiet  = flag.Bool("no-ascii", false, "don't print the code on the terminal")
	)
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "usage: go run scripts/gen-qr.go [flags] [ws://host:port]\n\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	server := strings.TrimSpace(flag.Arg(0))
	if server == "" {
		server = prompt("NATS server URL (ws:// or wss://): ")
	}
	if server == "" {
		log.Fatal("no server URL given")
	}
	if *name == "" && flag.NArg() == 0 {
		*name = prompt("Server name (optional): ")
	}

	link, err := webdist.JoinLink(*page, server, *name)
	if err != nil {
		log.Fatal(err)
	}
	// The one pairing that always fails: an https page may not open a plain
	// ws:// socket (mixed content).
	if pu, err := url.Parse(*page); err == nil && pu.Scheme == "https" && strings.HasPrefix(server, "ws://") {
		log.Printf("warning: %s is served over https, and a browser refuses a plain ws:// socket from an https page — use wss://, or serve the page over http", *page)
	}
	code, err := qr.Encode([]byte(link))
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println(link)
	fmt.Println()
	if !*quiet {
		fmt.Print(ansi(code, *border))
		fmt.Println()
	}
	if *out != "" {
		if err := writePNG(*out, code, *scale, *border); err != nil {
			log.Fatal(err)
		}
		fmt.Printf("wrote %s (%d×%d modules, version %d)\n", *out, code.Size, code.Size, code.Version)
	}
}

// prompt reads one line from the terminal, for the no-arguments run.
func prompt(q string) string {
	fmt.Print(q)
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return ""
	}
	return strings.TrimSpace(line)
}

// writePNG renders the code as black modules on white, scale pixels each,
// inside a quiet zone of border modules — the margin decoders need to find
// the code at all.
func writePNG(path string, m *qr.Code, scale, border int) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := png.Encode(f, m.Image(scale, border)); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// ansi draws the code for a terminal, two module rows per text line: the
// upper half block ▀ paints the top row in the foreground colour and the
// bottom row in the background one. The colours are stated rather than left
// to the terminal's palette, so the code comes out dark-on-light — what a
// scanner expects — whatever theme the terminal runs.
func ansi(m *qr.Code, border int) string {
	const (
		black = 0 // module set
		white = 7 // quiet zone and clear modules (bright, for contrast)
	)
	n := m.Size + 2*border
	at := func(row, col int) bool {
		row, col = row-border, col-border
		if row < 0 || col < 0 || row >= m.Size || col >= m.Size {
			return false
		}
		return m.Dark(row, col)
	}
	var b strings.Builder
	for row := 0; row < n; row += 2 {
		for col := 0; col < n; col++ {
			fg, bg := white, white
			if at(row, col) {
				fg = black
			}
			if row+1 < n && at(row+1, col) {
				bg = black
			}
			fmt.Fprintf(&b, "\x1b[%d;%dm▀", 30+fg, 100+bg)
		}
		b.WriteString("\x1b[0m\n")
	}
	return b.String()
}
