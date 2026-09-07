package main

import (
	"crypto/ecdh"
	"testing"
)

func TestRFC8291Receiver(t *testing.T) {
	decode := func(value string) []byte {
		t.Helper()
		raw, err := encoding.DecodeString(value)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	private, err := ecdh.P256().NewPrivateKey(decode("q1dXpw3UpT5VOmu_cf_v6ih07Aems3njxI-JWgLcM94"))
	if err != nil {
		t.Fatal(err)
	}
	auth := decode("BTBZMqHH6r4Tts7J_aSIgg")
	body := decode("DGv6ra1nlYgDCS1FRnbzlwAAEABBBP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27mlmlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A_yl95bQpu6cVPTpK4Mqgkf1CXztLVBSt2Ks3oZwbuwXPXLWyouBWLVWGNWQexSgSxsj_Qulcy4a-fN")
	plain, err := decrypt(private, auth, body)
	if err != nil || string(plain) != "When I grow up, I want to be a watermelon" {
		t.Fatal("RFC receiver vector differs", err)
	}
	body[len(body)-1] ^= 1
	if _, err := decrypt(private, auth, body); err == nil {
		t.Fatal("tampered ciphertext accepted")
	}
	if _, err := decrypt(private, auth, body[:20]); err == nil {
		t.Fatal("truncated ciphertext accepted")
	}
}
