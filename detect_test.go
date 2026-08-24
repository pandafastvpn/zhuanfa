package main

import "testing"

func TestDetectProtocolOpenVPN(t *testing.T) {
	udp := []byte{0x38, 0, 0, 0}
	if got := detectProtocol(udp); got != ProtoOpenVPN {
		t.Fatalf("UDP OpenVPN: got %q, want %q", got, ProtoOpenVPN)
	}

	tcp := []byte{0, 30, 0x38, 0, 0, 0}
	if got := detectProtocol(tcp); got != ProtoOpenVPN {
		t.Fatalf("TCP OpenVPN: got %q, want %q", got, ProtoOpenVPN)
	}
}

func TestDetectProtocolRejectsHTTP(t *testing.T) {
	httpPayload := []byte("GET / HTTP/1.1\\r\\n")
	if got := detectProtocol(httpPayload); got != ProtoUnknown {
		t.Fatalf("HTTP: got %q, want %q", got, ProtoUnknown)
	}
}
