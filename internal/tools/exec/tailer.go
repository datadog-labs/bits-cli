package exec

import (
	"strings"
	"unicode/utf8"
)

// headTailBuffer retains the beginning and end of one byte stream while
// continuing to consume all writes. One instance has a single writer.
type headTailBuffer struct {
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

func (b *headTailBuffer) snapshot(limit int) []byte {
	if limit <= 0 || b.total == 0 {
		return nil
	}
	if int64(limit) >= b.total {
		result := make([]byte, 0, int(b.total))
		result = append(result, b.head...)
		return b.appendTail(result, b.tailLen)
	}

	headLen := limit - limit/2
	tailLen := limit / 2
	result := make([]byte, 0, limit)
	result = append(result, b.head[:min(headLen, len(b.head))]...)
	return b.appendTail(result, min(tailLen, b.tailLen))
}

func (b *headTailBuffer) appendTail(dst []byte, count int) []byte {
	if count == 0 {
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

func boundedExecOutput(stdout, stderr *headTailBuffer) ExecOutput {
	stdoutBytes := stdout.snapshot(stdout.limit())
	stderrBytes := stderr.snapshot(stderr.limit())
	stdoutOmitted := stdout.total - int64(len(stdoutBytes))
	stderrOmitted := stderr.total - int64(len(stderrBytes))
	return ExecOutput{
		Stdout:             stdout.outputText(stdoutBytes),
		Stderr:             stderr.outputText(stderrBytes),
		StdoutOmittedBytes: stdoutOmitted,
		StderrOmittedBytes: stderrOmitted,
		Truncated:          stdoutOmitted != 0 || stderrOmitted != 0,
	}
}

// execOutputText defines the runner's text boundary. Command streams may carry
// arbitrary bytes, but tool results are UTF-8 text; invalid byte sequences are
// made visible as replacement characters before JSON serialization.
func execOutputText(bytes []byte) string {
	return strings.ToValidUTF8(string(bytes), "\uFFFD")
}

func (b *headTailBuffer) limit() int {
	return cap(b.head) + len(b.tail)
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

func (b *headTailBuffer) outputText(bytes []byte) string {
	if b.invalidUTF8 || len(b.partialUTF8) != 0 {
		return decodeExecOutput(bytes)
	}
	return execOutputText(bytes)
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
