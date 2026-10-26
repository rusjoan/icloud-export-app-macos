package icloud

import (
	"bytes"
	"math/big"
	"testing"
)

// Серверная сторона SRP-6a по тем же правилам — клиент и сервер должны сойтись на M1/HAMK.
func TestSRPHandshakeMatchesServer(t *testing.T) {
	for _, proto := range []string{"s2k", "s2k_fo"} {
		t.Run(proto, func(t *testing.T) {
			salt := []byte{0x00, 0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88, 0x99, 0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff}
			p, err := srpPassword("correct horse", proto, salt, 1000)
			if err != nil {
				t.Fatal(err)
			}
			c := newSRPClientWithSecret("user@example.com", big.NewInt(0x1234567890abcdef))

			width := len(srpN.Bytes())
			s := new(big.Int).SetBytes(salt).Bytes()
			x := hInt(0, s, hInt(0, []byte(":"), p).Bytes())
			v := new(big.Int).Exp(srpG, x, srpN)
			k := hInt(width, srpN.Bytes(), srpG.Bytes())
			b := big.NewInt(0xfedcba987654321)
			B := new(big.Int).Add(new(big.Int).Mul(k, v), new(big.Int).Exp(srpG, b, srpN))
			B.Mod(B, srpN)

			if err := c.process(p, salt, B.Bytes()); err != nil {
				t.Fatal(err)
			}

			u := hInt(width, c.A.Bytes(), B.Bytes())
			S := new(big.Int).Exp(new(big.Int).Mul(c.A, new(big.Int).Exp(v, u, srpN)), b, srpN)
			K := sha(S.Bytes())
			hN, hg := sha(srpN.Bytes()), sha(pad(srpG.Bytes(), width))
			xor := make([]byte, 32)
			for i := range xor {
				xor[i] = hN[i] ^ hg[i]
			}
			wantM := sha(xor, sha([]byte("user@example.com")), s, c.A.Bytes(), B.Bytes(), K)
			if !bytes.Equal(c.M1, wantM) {
				t.Fatalf("M1 mismatch")
			}
			if !bytes.Equal(c.HAMK, sha(c.A.Bytes(), wantM, K)) {
				t.Fatalf("HAMK mismatch")
			}
		})
	}
}

func TestSRPPasswordProtocols(t *testing.T) {
	salt := []byte("salt")
	a, _ := srpPassword("pw", "s2k", salt, 10)
	b, _ := srpPassword("pw", "s2k_fo", salt, 10)
	if len(a) != 32 || len(b) != 32 || bytes.Equal(a, b) {
		t.Fatalf("s2k and s2k_fo must differ and be 32 bytes")
	}
	if _, err := srpPassword("pw", "unknown", salt, 10); err == nil {
		t.Fatal("expected error for unknown protocol")
	}
}

func TestSRPRejectsZeroB(t *testing.T) {
	c := newSRPClientWithSecret("u", big.NewInt(5))
	if err := c.process([]byte("p"), []byte("s"), srpN.Bytes()); err == nil {
		t.Fatal("B = N must be rejected")
	}
}
