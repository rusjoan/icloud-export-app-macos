package icloud

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"math/big"
)

// SRP-6a в варианте, который ожидает idmsa.apple.com: группа RFC 5054 2048 бит,
// SHA-256, имя пользователя не входит в x, пароль предварительно прогоняется
// через PBKDF2 (протоколы s2k / s2k_fo). Сериализация чисел повторяет pysrp
// (минимальные big-endian байты без ведущих нулей), на нём построен pyicloud.

var (
	srpN, _ = new(big.Int).SetString(
		"AC6BDB41324A9A9BF166DE5E1389582FAF72B6651987EE07FC3192943DB56050A37329CBB4A099ED8193E0757767A13DD52312AB4B03310D"+
			"CD7F48A9DA04FD50E8083969EDB767B0CF6095179A163AB3661A05FBD5FAAAE82918A9962F0B93B855F97993EC975EEAA80D740ADBF4FF"+
			"747359D041D5C33EA71D281E446B14773BCA97B43A23FB801676BD207A436C6481F1D2B9078717461A5B9D32E688F87748544523B524B0D5"+
			"7D5EA77A2775D2ECFA032CFBDBF52FB3786160279004E57AE6AF874E7303CE53299CCC041C7BC308D82A5698F3A8D0C38271AE35F8E9DBFB"+
			"B694B5C803D89F7AE435DE236D525F54759B65E372FCD68EF20FA7111F9E4AFF73", 16)
	srpG = big.NewInt(2)
)

// srpClient — состояние одного рукопожатия.
type srpClient struct {
	user []byte
	a    *big.Int
	A    *big.Int
	M1   []byte
	HAMK []byte
}

func newSRPClient(user string) (*srpClient, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return nil, err
	}
	return newSRPClientWithSecret(user, new(big.Int).SetBytes(buf)), nil
}

func newSRPClientWithSecret(user string, a *big.Int) *srpClient {
	return &srpClient{user: []byte(user), a: a, A: new(big.Int).Exp(srpG, a, srpN)}
}

// srpPassword выводит «пароль» для SRP из пароля Apple ID.
func srpPassword(password, protocol string, salt []byte, iterations int) ([]byte, error) {
	h := sha256.Sum256([]byte(password))
	p := h[:]
	switch protocol {
	case "s2k":
	case "s2k_fo":
		p = []byte(hexLower(p))
	default:
		return nil, errors.New("неподдерживаемый протокол SRP: " + protocol)
	}
	return pbkdf2.Key(sha256.New, string(p), salt, iterations, 32)
}

// process вычисляет M1 и ожидаемое подтверждение сервера по salt и B.
func (c *srpClient) process(derivedPassword, salt, bBytes []byte) error {
	B := new(big.Int).SetBytes(bBytes)
	if new(big.Int).Mod(B, srpN).Sign() == 0 {
		return errors.New("SRP: некорректный B от сервера")
	}
	width := len(srpN.Bytes())
	u := hInt(width, c.A.Bytes(), B.Bytes())
	if u.Sign() == 0 {
		return errors.New("SRP: u = 0")
	}
	k := hInt(width, srpN.Bytes(), srpG.Bytes())
	s := new(big.Int).SetBytes(salt).Bytes() // pysrp теряет ведущие нули соли
	inner := hInt(0, []byte(":"), derivedPassword)
	x := hInt(0, s, inner.Bytes())

	v := new(big.Int).Exp(srpG, x, srpN)
	base := new(big.Int).Sub(B, new(big.Int).Mul(k, v))
	base.Mod(base, srpN)
	exp := new(big.Int).Add(c.a, new(big.Int).Mul(u, x))
	S := new(big.Int).Exp(base, exp, srpN)
	K := sha(S.Bytes())

	hN := sha(srpN.Bytes())
	hg := sha(pad(srpG.Bytes(), width))
	xor := make([]byte, len(hN))
	for i := range hN {
		xor[i] = hN[i] ^ hg[i]
	}
	c.M1 = sha(xor, sha(c.user), s, c.A.Bytes(), B.Bytes(), K)
	c.HAMK = sha(c.A.Bytes(), c.M1, K)
	return nil
}

func sha(parts ...[]byte) []byte {
	h := sha256.New()
	for _, p := range parts {
		h.Write(p)
	}
	return h.Sum(nil)
}

// hInt — H() из pysrp: при width > 0 каждый аргумент дополняется нулями слева.
func hInt(width int, parts ...[]byte) *big.Int {
	h := sha256.New()
	for _, p := range parts {
		if width > 0 {
			h.Write(pad(p, width))
		} else {
			h.Write(p)
		}
	}
	return new(big.Int).SetBytes(h.Sum(nil))
}

func pad(b []byte, width int) []byte {
	if len(b) >= width {
		return b
	}
	out := make([]byte, width)
	copy(out[width-len(b):], b)
	return out
}

func hexLower(b []byte) string {
	const digits = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, v := range b {
		out[i*2] = digits[v>>4]
		out[i*2+1] = digits[v&0x0f]
	}
	return string(out)
}
