package fasthttp

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"math"
	"net"
	"sync"
	"time"
	"unsafe"
)

// AppendIPv4 appends string representation of the given ip v4 to dst
// and returns the extended dst.
func AppendIPv4(dst []byte, ip net.IP) []byte {
	ip = ip.To4()
	if ip == nil {
		return append(dst, "non-v4 ip passed to AppendIPv4"...)
	}

	dst = AppendUint(dst, int(ip[0]))
	for i := 1; i < 4; i++ {
		dst = append(dst, '.')
		dst = AppendUint(dst, int(ip[i]))
	}
	return dst
}

// ParseIPv4 parses ip address from ipStr into dst and returns the extended dst.
func ParseIPv4(dst net.IP, ipStr []byte) (net.IP, error) {
	if len(dst) < net.IPv4len {
		dst = make([]byte, net.IPv4len)
	}
	copy(dst, net.IPv4zero)
	dst = dst.To4()
	if dst == nil {
		panic("BUG: dst must not be nil")
	}

	b := ipStr
	for i := 0; i < 3; i++ {
		n := bytes.IndexByte(b, '.')
		if n < 0 {
			return dst, fmt.Errorf("cannot find dot in ipStr %q", ipStr)
		}
		v, err := ParseUint(b[:n])
		if err != nil {
			return dst, fmt.Errorf("cannot parse ipStr %q: %s", ipStr, err)
		}
		if v > 255 {
			return dst, fmt.Errorf("cannot parse ipStr %q: ip part cannot exceed 255: parsed %d", ipStr, v)
		}
		dst[i] = byte(v)
		b = b[n+1:]
	}
	v, err := ParseUint(b)
	if err != nil {
		return dst, fmt.Errorf("cannot parse ipStr %q: %s", ipStr, err)
	}
	if v > 255 {
		return dst, fmt.Errorf("cannot parse ipStr %q: ip part cannot exceed 255: parsed %d", ipStr, v)
	}
	dst[3] = byte(v)

	return dst, nil
}

// AppendHTTPDate appends HTTP-compliant (RFC1123) representation of date
// to dst and returns the extended dst.
func AppendHTTPDate(dst []byte, date time.Time) []byte {
	dst = date.In(time.UTC).AppendFormat(dst, time.RFC1123)
	copy(dst[len(dst)-3:], strGMT)
	return dst
}

// ParseHTTPDate parses HTTP-compliant (RFC1123) date.
func ParseHTTPDate(date []byte) (time.Time, error) {
	return time.Parse(time.RFC1123, unsafeBytesToStr(date))
}

// AppendUint appends n to dst and returns the extended dst.
func AppendUint(dst []byte, n int) []byte {
	if n < 0 {
		panic("BUG: int must be positive")
	}

	var b [20]byte
	buf := b[:]
	i := len(buf)
	var q int
	for n >= 10 {
		i--
		q = n / 10
		buf[i] = '0' + byte(n-q*10)
		n = q
	}
	i--
	buf[i] = '0' + byte(n)

	dst = append(dst, buf[i:]...)
	return dst
}

// ParseUint parses uint from buf.
func ParseUint(buf []byte) (int, error) {
	v, n, err := parseUintBuf(buf)
	if n != len(buf) {
		return -1, fmt.Errorf("only %d bytes out of %d bytes exhausted when parsing int %q", n, len(buf), buf)
	}
	return v, err
}

func parseUintBuf(b []byte) (int, int, error) {
	n := len(b)
	if n == 0 {
		return -1, 0, fmt.Errorf("empty integer")
	}
	v := 0
	for i := 0; i < n; i++ {
		c := b[i]
		k := c - '0'
		if k > 9 {
			if i == 0 {
				return -1, i, fmt.Errorf("unexpected first char %c. Expected 0-9", c)
			}
			return v, i, nil
		}
		if i >= maxIntChars {
			return -1, i, fmt.Errorf("too long int %q", b[:i+1])
		}
		v = 10*v + int(k)
	}
	return v, n, nil
}

// ParseUfloat parses unsigned float from buf.
func ParseUfloat(buf []byte) (float64, error) {
	if len(buf) == 0 {
		return -1, fmt.Errorf("empty float number")
	}
	b := buf
	var v uint64
	var offset = 1.0
	var pointFound bool
	for i, c := range b {
		if c < '0' || c > '9' {
			if c == '.' {
				if pointFound {
					return -1, fmt.Errorf("duplicate point found in %q", buf)
				}
				pointFound = true
				continue
			}
			if c == 'e' || c == 'E' {
				if i+1 >= len(b) {
					return -1, fmt.Errorf("unexpected end of float after %c. num=%q", c, buf)
				}
				b = b[i+1:]
				minus := -1
				switch b[0] {
				case '+':
					b = b[1:]
					minus = 1
				case '-':
					b = b[1:]
				default:
					minus = 1
				}
				vv, err := ParseUint(b)
				if err != nil {
					return -1, fmt.Errorf("cannot parse exponent part of %q: %s", buf, err)
				}
				return float64(v) * offset * math.Pow10(minus*int(vv)), nil
			}
			return -1, fmt.Errorf("unexpected char found %c in %q", c, buf)
		}
		v = 10*v + uint64(c-'0')
		if pointFound {
			offset /= 10
		}
	}
	return float64(v) * offset, nil
}

func readHexInt(r *bufio.Reader) (int, error) {
	n := 0
	i := 0
	var k int
	for {
		c, err := r.ReadByte()
		if err != nil {
			if err == io.EOF && i > 0 {
				return n, nil
			}
			return -1, err
		}
		k = hexbyte2int(c)
		if k < 0 {
			if i == 0 {
				return -1, fmt.Errorf("cannot read hex num from empty string")
			}
			r.UnreadByte()
			return n, nil
		}
		if i >= maxHexIntChars {
			return -1, fmt.Errorf("cannot read hex num with more than %d digits", maxHexIntChars)
		}
		n = (n << 4) | k
		i++
	}
}

var hexIntBufPool sync.Pool

func writeHexInt(w *bufio.Writer, n int) error {
	if n < 0 {
		panic("BUG: int must be positive")
	}

	v := hexIntBufPool.Get()
	if v == nil {
		v = make([]byte, maxHexIntChars+1)
	}
	buf := v.([]byte)
	i := len(buf) - 1
	for {
		buf[i] = int2hexbyte(n & 0xf)
		n >>= 4
		if n == 0 {
			break
		}
		i--
	}
	_, err := w.Write(buf[i:])
	hexIntBufPool.Put(v)
	return err
}

func int2hexbyte(n int) byte {
	if n < 10 {
		return '0' + byte(n)
	}
	return 'a' + byte(n) - 10
}

func hexCharUpper(c byte) byte {
	if c < 10 {
		return '0' + c
	}
	return c - 10 + 'A'
}

var hex2intTable = func() []byte {
	b := make([]byte, 255)
	for i := byte(0); i < 255; i++ {
		c := byte(0)
		if i >= '0' && i <= '9' {
			c = 1 + i - '0'
		} else if i >= 'a' && i <= 'f' {
			c = 1 + i - 'a' + 10
		} else if i >= 'A' && i <= 'F' {
			c = 1 + i - 'A' + 10
		}
		b[i] = c
	}
	return b
}()

func hexbyte2int(c byte) int {
	return int(hex2intTable[c]) - 1
}

const toLower = 'a' - 'A'

func uppercaseByte(p *byte) {
	c := *p
	if c >= 'a' && c <= 'z' {
		*p = c - toLower
	}
}

func lowercaseByte(p *byte) {
	c := *p
	if c >= 'A' && c <= 'Z' {
		*p = c + toLower
	}
}

func lowercaseBytes(b []byte) {
	for i, n := 0, len(b); i < n; i++ {
		lowercaseByte(&b[i])
	}
}

// unsafeBytesToStr converts byte slice to a string without memory allocation.
// See https://groups.google.com/forum/#!msg/Golang-Nuts/ENgbUzYvCuU/90yGx7GUAgAJ .
//
// Note it may break if string and/or slice header will change
// in the future go versions.
func unsafeBytesToStr(b []byte) string {
	return *(*string)(unsafe.Pointer(&b))
}

// AppendQuotedArg appends url-encoded src to dst and returns appended dst.
func AppendQuotedArg(dst, src []byte) []byte {
	for _, c := range src {
		// See http://www.w3.org/TR/html5/forms.html#form-submission-algorithm
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
			c == '*' || c == '-' || c == '.' || c == '_' {
			dst = append(dst, c)
		} else {
			dst = append(dst, '%', hexCharUpper(c>>4), hexCharUpper(c&15))
		}
	}
	return dst
}

func appendQuotedPath(dst, src []byte) []byte {
	for _, c := range src {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
			c == '/' || c == '.' || c == ',' || c == '=' || c == ':' || c == '&' || c == '~' || c == '-' || c == '_' {
			dst = append(dst, c)
		} else {
			dst = append(dst, '%', hexCharUpper(c>>4), hexCharUpper(c&15))
		}
	}
	return dst
}

// EqualBytesStr returns true if string(b) == s.
//
// This function has no performance benefits comparing to string(b) == s.
// It is left here for backwards compatibility only.
//
// This function is deperecated and may be deleted soon.
func EqualBytesStr(b []byte, s string) bool {
	return string(b) == s
}

// AppendBytesStr appends src to dst and returns the extended dst.
//
// This function has no performance benefits comparing to append(dst, src...).
// It is left here for backwards compatibility only.
//
// This function is deprecated and may be deleted soon.
func AppendBytesStr(dst []byte, src string) []byte {
	return append(dst, src...)
}