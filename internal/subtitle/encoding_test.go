package subtitle

import (
	"encoding/binary"
	"strings"
	"testing"
	"time"
	"unicode/utf16"
)

func TestTimedTextDecodesUTF16AndMissingSeparators(t *testing.T) {
	input := "1\r\n00:00:20,000 --> 00:00:22,000\r\nবাংলা dialogue\r\n2\r\n00:00:10,000 --> 00:00:12,000\r\nAnother line\r\n"
	for _, order := range []binary.ByteOrder{binary.LittleEndian, binary.BigEndian} {
		units := utf16.Encode([]rune(input))
		data := make([]byte, 2+2*len(units))
		order.PutUint16(data, 0xfeff)
		for i, unit := range units {
			order.PutUint16(data[2+i*2:], unit)
		}
		cues, err := parseTimedText(data)
		if err != nil || len(cues) != 2 {
			t.Fatalf("parse = %v, %v", cues, err)
		}
		if cues[0].Start != 10*time.Second || strings.Join(cues[1].Text, " ") != "বাংলা dialogue" {
			t.Fatalf("text/order changed: %+v", cues)
		}
	}
}

func TestTimedTextRejectsTruncatedEncoding(t *testing.T) {
	for _, data := range [][]byte{{0xff, 0xfe, 0x41}, {0x81, 0x82, 0xff}} {
		if _, err := parseTimedText(data); err == nil {
			t.Fatal("invalid encoding accepted")
		}
	}
}
