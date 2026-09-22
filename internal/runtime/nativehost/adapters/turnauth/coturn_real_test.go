package turnauth

import (
	"net"
	"os"
	"testing"
	"time"

	"github.com/pion/turn/v4"
)

func TestRealCoturnCapabilityExpiryAndForgery(t *testing.T) {
	address := os.Getenv("WORKOS_TURN_TEST_ADDRESS")
	if address == "" {
		t.Skip("requires isolated network-continuity coturn")
	}
	i, err := New("relay", "turn:"+address, os.Getenv("WORKOS_TURN_TEST_SECRET_FILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"valid", "expired", "forged"} {
		t.Run(kind, func(t *testing.T) {
			i.now = time.Now
			if kind == "expired" {
				i.now = func() time.Time { return time.Now().Add(-2 * Lifetime) }
			}
			capability, err := i.Issue()
			if err != nil {
				t.Fatal(err)
			}
			credential := capability.Servers[0]
			if kind == "forged" {
				credential.Credential = "invalid-fixture-capability"
			}
			socket, err := net.ListenPacket("udp4", "0.0.0.0:0")
			if err != nil {
				t.Fatal(err)
			}
			defer socket.Close()
			client, err := turn.NewClient(&turn.ClientConfig{TURNServerAddr: address, Conn: socket, Username: credential.Username, Password: credential.Credential, Realm: "workos.fixture", RTO: 100 * time.Millisecond})
			if err != nil {
				t.Fatal("TURN client configuration failed")
			}
			defer client.Close()
			if err := client.Listen(); err != nil {
				t.Fatal("TURN client listener failed")
			}
			relay, err := client.Allocate()
			if relay != nil {
				defer relay.Close()
			}
			if kind == "valid" && err != nil {
				t.Fatal("valid short-lived TURN capability refused")
			}
			if kind != "valid" && err == nil {
				t.Fatal("expired or forged TURN capability accepted")
			}
		})
	}
	t.Run("bidirectional-relay", func(t *testing.T) {
		i.now = time.Now
		var peers []net.PacketConn
		var clients []*turn.Client
		for range 2 {
			capability, err := i.Issue()
			if err != nil {
				t.Fatal(err)
			}
			credential := capability.Servers[0]
			socket, err := net.ListenPacket("udp4", "0.0.0.0:0")
			if err != nil {
				t.Fatal(err)
			}
			defer socket.Close()
			client, err := turn.NewClient(&turn.ClientConfig{TURNServerAddr: address, Conn: socket, Username: credential.Username, Password: credential.Credential, Realm: "workos.fixture", RTO: 100 * time.Millisecond})
			if err != nil {
				t.Fatal("TURN client configuration failed")
			}
			defer client.Close()
			if err := client.Listen(); err != nil {
				t.Fatal("TURN client listener failed")
			}
			relay, err := client.Allocate()
			if err != nil {
				t.Fatal("TURN allocation failed")
			}
			defer relay.Close()
			clients = append(clients, client)
			peers = append(peers, relay)
		}
		for index, client := range clients {
			if err := client.CreatePermission(peers[1-index].LocalAddr()); err != nil {
				t.Fatal("TURN peer permission refused")
			}
			if err := client.CreatePermission(&net.UDPAddr{IP: net.ParseIP("192.0.2.1"), Port: 9999}); err == nil {
				t.Fatal("TURN allowed a destination outside the deployment relay addresses")
			}
		}
		for index, peer := range peers {
			other := peers[1-index]
			if _, err := peer.WriteTo([]byte("fixture-relay"), other.LocalAddr()); err != nil {
				t.Fatal("TURN write failed")
			}
			if err := other.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
				t.Fatal(err)
			}
			buffer := make([]byte, 128)
			n, _, err := other.ReadFrom(buffer)
			if err != nil || string(buffer[:n]) != "fixture-relay" {
				t.Fatal("TURN relay did not carry fixture data")
			}
		}
	})
}
