package ingest

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
)

// maxLineBytes es el tope por línea de un transcript. Las sesiones pueden traer
// imágenes en base64 o salidas de tools enormes en una sola línea, así que
// damos margen amplio.
const maxLineBytes = 64 * 1024 * 1024

// lineReader itera las líneas no vacías de un transcript JSONL. Reemplaza a
// bufio.Scanner, que con líneas de varios MB re-busca el salto de línea desde
// el inicio de la línea en cada lectura (costo cuadrático); ReadSlice solo
// busca en los bytes nuevos. El slice de Bytes se reutiliza: vale hasta el
// próximo Next.
type lineReader struct {
	r    *bufio.Reader
	buf  []byte // acumula las líneas que no caben en el buffer del reader
	line []byte
	err  error
}

func newLineReader(r io.Reader) *lineReader {
	return &lineReader{r: bufio.NewReaderSize(r, 1<<20)}
}

// Next avanza a la próxima línea no vacía (sin espacios en los bordes).
// Devuelve false al terminar el input o ante un error (ver Err).
func (l *lineReader) Next() bool {
	for l.err == nil {
		chunk, err := l.r.ReadSlice('\n')
		line := chunk
		if errors.Is(err, bufio.ErrBufferFull) || len(l.buf) > 0 {
			// Línea más larga que el buffer: acumularla hasta el salto.
			if len(l.buf)+len(chunk) > maxLineBytes {
				l.err = fmt.Errorf("line exceeds %d bytes", maxLineBytes)
				return false
			}
			l.buf = append(l.buf, chunk...)
			if errors.Is(err, bufio.ErrBufferFull) {
				continue
			}
			line = l.buf
		}
		if err != nil && !errors.Is(err, io.EOF) {
			l.err = err
			return false
		}
		trimmed := bytes.TrimSpace(line)
		l.buf = l.buf[:0]
		if err != nil { // io.EOF: la última línea puede no tener salto
			l.err = io.EOF
		}
		if len(trimmed) > 0 {
			l.line = trimmed
			return true
		}
	}
	return false
}

// Bytes devuelve la línea actual; vale solo hasta el próximo Next.
func (l *lineReader) Bytes() []byte { return l.line }

// Err devuelve el primer error de lectura (nil si el input terminó bien).
func (l *lineReader) Err() error {
	if errors.Is(l.err, io.EOF) {
		return nil
	}
	return l.err
}
