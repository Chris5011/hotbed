package dnsfence

import (
	"strings"
)

// Minimal DNS wire helpers (stdlib-only): decode a single-question A/AAAA
// query, synthesize REFUSED answers, pull IPv4 records out of an upstream
// answer. Deliberately fail-closed: any malformed packet is refused, never
// guessed.

const (
	qtypeA    = 1
	qtypeAAAA = 28
	rcREFUSED = 5
)

// query is one decoded client query.
type query struct {
	id     uint16 // transaction id (echoed in responses)
	qd     []byte // raw question section (echoed as-is)
	domain string // lowercased qname, no trailing dot
	hasA   bool   // asked for A (false = AAAA)
	recDes bool   // RD bit from the header
}

// parseQuery decodes the first question of a DNS packet. Questions never
// use name compression, so anything that looks like a pointer here is junk.
func parseQuery(b []byte) (query, bool) {
	if len(b) < 12 {
		return query{}, false
	}
	flags := uint16(b[2])<<8 | uint16(b[3])
	if flags&0x8000 != 0 || b[5] != 1 { // response, or not exactly one question
		return query{}, false
	}
	var sb []byte
	i := 12
	for {
		if i >= len(b) {
			return query{}, false
		}
		l := int(b[i])
		if l == 0 {
			i++
			break
		}
		if l >= 0xC0 || i+1+l >= len(b) {
			return query{}, false
		}
		sb = append(sb, b[i+1:i+1+l]...)
		sb = append(sb, '.')
		i += 1 + l
	}
	if i+4 > len(b) {
		return query{}, false
	}
	qtype := int(b[i])<<8 | int(b[i+1])
	if qtype != qtypeA && qtype != qtypeAAAA {
		return query{}, false
	}
	i += 4 // past qtype/qclass
	name := strings.ToLower(strings.TrimSuffix(string(sb), "."))
	if name == "" {
		return query{}, false
	}
	return query{
		id:     uint16(b[0])<<8 | uint16(b[1]),
		qd:     append([]byte{}, b[12:i]...),
		domain: name,
		hasA:   qtype == qtypeA,
		recDes: flags&0x0100 != 0,
	}, true
}

// refusedA synthesizes a REFUSED (rcode 5, zero answers) response echoing
// the question.
func (q query) refusedA() []byte {
	flags := []byte{0x80, 0x05}
	if q.recDes {
		flags[0] = 0x81 // QR + RD
	}
	hdr := append([]byte{byte(q.id >> 8), byte(q.id)}, flags...)
	hdr = append(hdr, 0, 1, 0, 0, 0, 0, 0, 0)
	return append(hdr, q.qd...)
}

// noRecords synthesizes a NOERROR answer with zero records (used for A/AAAA
// domains that resolved elsewhere but returned nothing for this type).
func (q query) noRecords() []byte {
	flags := []byte{0x80, 0x00}
	if q.recDes {
		flags[0] = 0x81
	}
	flags[1] |= 0x80 // RA
	hdr := append([]byte{byte(q.id >> 8), byte(q.id)}, flags...)
	hdr = append(hdr, 0, 1, 0, 0, 0, 0, 0, 0)
	return append(hdr, q.qd...)
}

// withA builds a NOERROR response carrying the given A rdata blobs. Only
// meaningful for A queries — AAAA callers use Refused/NoRecords.
func (q query) withA(rdata [][]byte) []byte {
	if !q.hasA {
		return q.noRecords()
	}
	flags := []byte{0x80, 0x00}
	if q.recDes {
		flags[0] = 0x81
	}
	flags[1] |= 0x80 // RA
	hdr := append([]byte{byte(q.id >> 8), byte(q.id)}, flags...)
	anc := len(rdata)
	if anc > 40 {
		anc = 40
	}
	hdr = append(hdr, 0, 1, 0, byte(anc), 0, 0, 0, 0)
	out := append(hdr, q.qd...)
	for _, a := range rdata[:anc] {
		out = append(out,
			0xC0, 0x0C, // name: pointer to offset 12 (the echoed qname)
			0, byte(qtypeA), // type A
			0, 1, // class IN
			0, 0, 0, 30, // TTL 30s
			0, 4)
		out = append(out, a...)
	}
	return out
}

// extractIPv4 walks an upstream response and returns the IPv4 rdata blobs
// from its answer section (compressed names are skipped, not decoded).
func extractIPv4(b []byte) [][]byte {
	if len(b) < 12 {
		return nil
	}
	i, ok := skipQuestions(b, 12, int(b[4])<<8|int(b[5]))
	if !ok {
		return nil
	}
	var out [][]byte
	an := int(b[6])<<8 | int(b[7])
	for k := 0; k < an; k++ {
		i, ok = skipName(b, i)
		if !ok || i+10 > len(b) {
			return out
		}
		typ := int(b[i])<<8 | int(b[i+1])
		rdl := int(b[i+8])<<8 | int(b[i+9])
		i += 10
		if i+rdl > len(b) {
			return out
		}
		if typ == qtypeA && rdl == 4 {
			out = append(out, append([]byte{}, b[i:i+4]...))
		}
		i += rdl
	}
	return out
}

// skipQuestions advances past the question section.
func skipQuestions(b []byte, i, n int) (int, bool) {
	for k := 0; k < n; k++ {
		var ok bool
		i, ok = skipName(b, i)
		if !ok || i+4 > len(b) {
			return 0, false
		}
		i += 4
	}
	return i, true
}

// skipName walks a (possibly compressed) name and returns the index just
// after it; a pointer ends the walk (we never need the final labels).
func skipName(b []byte, i int) (int, bool) {
	for {
		if i >= len(b) {
			return 0, false
		}
		l := int(b[i])
		switch {
		case l&0xC0 == 0xC0:
			if i+2 > len(b) {
				return 0, false
			}
			return i + 2, true
		case l == 0:
			return i + 1, true
		default:
			i += 1 + l
		}
	}
}
