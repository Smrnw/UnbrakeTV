// fakeadb is a tiny ADB device emulator for developing Unbrake TV without a TV.
// It speaks the real ADB protocol (including RSA authentication) and answers
// shell commands with data from a Xiaomi TV.
//
//	go run ./cmd/fakeadb -listen 127.0.0.1:5555
//
// Wireless debugging (Android 11+) with pairing by code:
//
//	go run ./cmd/fakeadb -listen 127.0.0.1:37555 -wireless -pair 127.0.0.1:37556 -code 123456
package main

import (
	"flag"
	"log"

	"github.com/Smrnw/UnbrakeTV/internal/faketv"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:5555", "ADB address")
	wireless := flag.Bool("wireless", false, "emulate wireless debugging: TLS, only paired computers are accepted")
	pair := flag.String("pair", "127.0.0.1:37556", "pairing address (with -wireless)")
	code := flag.String("code", "123456", "pairing code (with -wireless)")
	flag.Parse()

	t, err := faketv.New(faketv.Options{Wireless: *wireless, PairCode: *code, Logf: faketv.StdLogf})
	if err != nil {
		log.Fatal(err)
	}
	addr, err := t.Listen(*listen)
	if err != nil {
		log.Fatal(err)
	}
	log.Println("fake TV listening on", addr)
	if *wireless {
		p, err := t.ListenPairing(*pair)
		if err != nil {
			log.Fatal(err)
		}
		log.Println("pairing on", p, "code", *code)
	}
	select {}
}
