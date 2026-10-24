package backup

import (
	"bufio"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"tawanzu/internal/state"
)

// Formato cifrado (extensión .enc): "TWZENC1\n" + prefijo de nonce (4 bytes) y después fragmentos
// [longitud uint32][AES-256-GCM(datos)]. El último fragmento lleva AAD=1: si falta, el archivo está truncado.
const (
	encMagic = "TWZENC1\n"
	encChunk = 64 * 1024
)

// KeyPath es el archivo de clave de los backups cifrados (hex de 32 bytes, permisos 0600).
func KeyPath() string {
	if v := os.Getenv("TAWANZU_BACKUP_KEY"); v != "" {
		return v
	}
	return filepath.Join(state.Home(), "backup.key")
}

func loadKey(create bool) ([]byte, error) {
	p := KeyPath()
	b, err := os.ReadFile(p)
	if err == nil {
		k, err := hex.DecodeString(strings.TrimSpace(string(b)))
		if err != nil || len(k) != 32 {
			return nil, fmt.Errorf("clave de backup inválida en %s (se esperan 64 caracteres hex)", p)
		}
		return k, nil
	}
	if !create || !os.IsNotExist(err) {
		return nil, fmt.Errorf("no se pudo leer la clave de backup %s: %w", p, err)
	}
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(p, []byte(hex.EncodeToString(k)+"\n"), 0o600); err != nil {
		return nil, err
	}
	return k, nil
}

func newAEAD(key []byte) (cipher.AEAD, error) {
	blk, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(blk)
}

type encWriter struct {
	w      io.Writer
	aead   cipher.AEAD
	prefix [4]byte
	n      uint64
	buf    []byte
	closed bool
}

func newEncWriter(w io.Writer, key []byte) (*encWriter, error) {
	a, err := newAEAD(key)
	if err != nil {
		return nil, err
	}
	e := &encWriter{w: w, aead: a}
	if _, err := rand.Read(e.prefix[:]); err != nil {
		return nil, err
	}
	if _, err := w.Write([]byte(encMagic)); err != nil {
		return nil, err
	}
	if _, err := w.Write(e.prefix[:]); err != nil {
		return nil, err
	}
	return e, nil
}

func (e *encWriter) nonce() []byte {
	n := make([]byte, e.aead.NonceSize())
	copy(n, e.prefix[:])
	binary.BigEndian.PutUint64(n[4:], e.n)
	e.n++
	return n
}

func (e *encWriter) flush(final bool) error {
	aad := []byte{0}
	if final {
		aad[0] = 1
	}
	ct := e.aead.Seal(nil, e.nonce(), e.buf, aad)
	var l [4]byte
	binary.BigEndian.PutUint32(l[:], uint32(len(ct)))
	if _, err := e.w.Write(l[:]); err != nil {
		return err
	}
	_, err := e.w.Write(ct)
	e.buf = e.buf[:0]
	return err
}

func (e *encWriter) Write(p []byte) (int, error) {
	total := len(p)
	for len(p) > 0 {
		room := encChunk - len(e.buf)
		if room > len(p) {
			room = len(p)
		}
		e.buf = append(e.buf, p[:room]...)
		p = p[room:]
		if len(e.buf) == encChunk {
			if err := e.flush(false); err != nil {
				return 0, err
			}
		}
	}
	return total, nil
}

// Close escribe el fragmento final (obligatorio para poder descifrar).
func (e *encWriter) Close() error {
	if e.closed {
		return nil
	}
	e.closed = true
	return e.flush(true)
}

type decReader struct {
	r    *bufio.Reader
	aead cipher.AEAD
	pre  [4]byte
	n    uint64
	buf  []byte
	done bool
}

func newDecReader(r *bufio.Reader, key []byte) (*decReader, error) {
	a, err := newAEAD(key)
	if err != nil {
		return nil, err
	}
	hd := make([]byte, len(encMagic))
	if _, err := io.ReadFull(r, hd); err != nil || string(hd) != encMagic {
		return nil, errors.New("el archivo no tiene el formato cifrado de Tawanzu")
	}
	d := &decReader{r: r, aead: a}
	if _, err := io.ReadFull(r, d.pre[:]); err != nil {
		return nil, err
	}
	return d, nil
}

func (d *decReader) Read(p []byte) (int, error) {
	for len(d.buf) == 0 {
		if d.done {
			return 0, io.EOF
		}
		var l [4]byte
		if _, err := io.ReadFull(d.r, l[:]); err != nil {
			return 0, fmt.Errorf("archivo cifrado truncado: %w", io.ErrUnexpectedEOF)
		}
		size := binary.BigEndian.Uint32(l[:])
		if size > encChunk+64 {
			return 0, errors.New("fragmento cifrado inválido")
		}
		ct := make([]byte, size)
		if _, err := io.ReadFull(d.r, ct); err != nil {
			return 0, fmt.Errorf("archivo cifrado truncado: %w", io.ErrUnexpectedEOF)
		}
		nonce := make([]byte, d.aead.NonceSize())
		copy(nonce, d.pre[:])
		binary.BigEndian.PutUint64(nonce[4:], d.n)
		d.n++
		pt, err := d.aead.Open(nil, nonce, ct, []byte{0})
		if err != nil {
			pt, err = d.aead.Open(nil, nonce, ct, []byte{1})
			if err != nil {
				return 0, errors.New("no se pudo descifrar: clave incorrecta o archivo alterado")
			}
			d.done = true
		}
		d.buf = pt
	}
	n := copy(p, d.buf)
	d.buf = d.buf[n:]
	return n, nil
}
