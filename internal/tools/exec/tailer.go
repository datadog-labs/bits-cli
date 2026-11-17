package exec

import (
	"strings"
	"sync"
	"unicode/utf8"
)

// headTailBuffer retains the beginning and end of one byte stream while
// continuing to consume all writes. One instance has a single writer; reads
// may run while the command still writes.
type headTailBuffer struct {
	mu        sync.Mutex
	head      []byte
	tail      []byte
	tailStart int
	tailLen   int
	total     int64

	// Track validity before truncation: a cut UTF-8 rune must not cause an
	// otherwise UTF-8 stream to be decoded as a Windows code page.
	invalidUTF8 bool
	partialUTF8 []byte
}

func newHeadTailBuffer(limit int) *headTailBuffer {
	if limit < 0 {
		limit = 0
	}
	return &headTailBuffer{
		head: make([]byte, 0, limit-limit/2),
		tail: make([]byte, limit/2),
	}
}

func (b *headTailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.observeUTF8(p)
	written := len(p)
	b.total += int64(written)

	if available := cap(b.head) - len(b.head); available > 0 {
		take := min(available, len(p))
		b.head = append(b.head, p[:take]...)
		p = p[take:]
	}
	b.writeTail(p)
	return written, nil
}

func (b *headTailBuffer) writeTail(p []byte) {
	capacity := len(b.tail)
	if capacity == 0 || len(p) == 0 {
		return
	}
	if len(p) >= capacity {
		copy(b.tail, p[len(p)-capacity:])
		b.tailStart = 0
		b.tailLen = capacity
		return
	}

	if b.tailLen < capacity {
		take := min(capacity-b.tailLen, len(p))
		b.copyIntoTail((b.tailStart+b.tailLen)%capacity, p[:take])
		b.tailLen += take
		p = p[take:]
	}
	if len(p) == 0 {
		return
	}
	b.copyIntoTail(b.tailStart, p)
	b.tailStart = (b.tailStart + len(p)) % capacity
}

func (b *headTailBuffer) copyIntoTail(start int, p []byte) {
	first := min(len(p), len(b.tail)-start)
	copy(b.tail[start:], p[:first])
	copy(b.tail, p[first:])
}

// since returns the retained text written at or after offset, how many of
// those bytes were not retained, and the offset the next read starts from.
// The head and tail never overlap: the tail holds the latest bytes past the
// head. Until the stream is final, a trailing incomplete UTF-8 sequence is
// left for the next read so a character split across writes is not mangled.
func (b *headTailBuffer) since(offset int64, final bool) (text string, omitted int64, next int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	headLen := int64(len(b.head))
	tailStart := b.total - int64(b.tailLen)
	var data []byte
	if offset < headLen {
		data = append(data, b.head[offset:]...)
	}
	if gapStart := max(offset, headLen); gapStart < tailStart {
		omitted = tailStart - gapStart
	}
	data = b.appendTail(data, int(b.total-max(offset, tailStart, headLen)))
	next = b.total
	// Hold a character split across writes for the read that completes it.
	if !final {
		held := incompleteUTF8Suffix(data)
		if b.invalidUTF8 {
			held = incompleteLegacySuffix(data)
		}
		data, next = data[:len(data)-held], next-int64(held)
	}
	return b.outputText(data), omitted, next
}

// incompleteUTF8Suffix is the length of a trailing UTF-8 sequence that more
// bytes could still complete.
func incompleteUTF8Suffix(data []byte) int {
	for n := 1; n <= min(len(data), utf8.UTFMax-1); n++ {
		c := data[len(data)-n]
		if c < utf8.RuneSelf {
			return 0
		}
		if utf8.RuneStart(c) {
			if utf8.FullRune(data[len(data)-n:]) {
				return 0
			}
			return n
		}
	}
	return 0
}

func (b *headTailBuffer) appendTail(dst []byte, count int) []byte {
	if count <= 0 {
		return dst
	}
	start := (b.tailStart + b.tailLen - count) % len(b.tail)
	first := min(count, len(b.tail)-start)
	dst = append(dst, b.tail[start:start+first]...)
	if first < count {
		dst = append(dst, b.tail[:count-first]...)
	}
	return dst
}

// readExecOutput returns both streams' output past cursor and advances it.
func readExecOutput(stdout, stderr *headTailBuffer, cursor *[2]int64, final bool) ExecOutput {
	var output ExecOutput
	output.Stdout, output.StdoutOmittedBytes, cursor[0] = stdout.since(cursor[0], final)
	output.Stderr, output.StderrOmittedBytes, cursor[1] = stderr.since(cursor[1], final)
	output.Truncated = output.StdoutOmittedBytes != 0 || output.StderrOmittedBytes != 0
	return output
}

// execOutputText defines the runner's text boundary. Command streams may carry
// arbitrary bytes, but tool results are UTF-8 text; invalid byte sequences are
// made visible as replacement characters before JSON serialization.
func execOutputText(bytes []byte) string {
	return strings.ToValidUTF8(string(bytes), "\uFFFD")
}

// splitOutputLimit gives each stream a fixed share so concurrent collection
// never retains more than the combined cap. A quiet stream deliberately does
// not donate its share: dynamic donation would make retained head/tail bytes
// depend on cross-stream write scheduling. An odd extra byte goes to stdout.
func splitOutputLimit(limit int) (int, int) {
	if limit <= 0 {
		return 0, 0
	}
	stderr := limit / 2
	return limit - stderr, stderr
}

// outputText decodes a read with the encoding decided once for the stream:
// UTF-8 until its first invalid byte, the legacy code page from then on, even
// for a read that alone is valid UTF-8.
// NOTE: reads are not revisited, so text read as UTF-8 before that byte stays
// as read; and Windows holds no split 4-byte GB18030 character across reads.
func (b *headTailBuffer) outputText(bytes []byte) string {
	switch {
	case b.invalidUTF8:
		return decodeLegacyOutput(bytes)
	case len(b.partialUTF8) != 0:
		return decodeExecOutput(bytes)
	default:
		return execOutputText(bytes)
	}
}

func (b *headTailBuffer) observeUTF8(p []byte) {
	if b.invalidUTF8 {
		return
	}
	if len(b.partialUTF8) != 0 {
		for len(p) > 0 && !utf8.FullRune(b.partialUTF8) {
			b.partialUTF8 = append(b.partialUTF8, p[0])
			p = p[1:]
		}
		if !utf8.FullRune(b.partialUTF8) {
			return
		}
		_, size := utf8.DecodeRune(b.partialUTF8)
		if size == 1 {
			b.invalidUTF8 = true
			return
		}
		b.partialUTF8 = b.partialUTF8[:0]
	}
	if utf8.Valid(p) {
		return
	}
	for len(p) > 0 {
		if !utf8.FullRune(p) {
			b.partialUTF8 = append(b.partialUTF8[:0], p...)
			return
		}
		_, size := utf8.DecodeRune(p)
		if size == 1 && p[0] >= utf8.RuneSelf {
			b.invalidUTF8 = true
			return
		}
		p = p[size:]
	}
}
