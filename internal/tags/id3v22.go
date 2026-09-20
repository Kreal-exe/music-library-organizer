package tags

// ID3v2.2 support.
//
// Version 2.2 of the standard names its frames with three letters instead of
// four and sizes them with three bytes instead of four. It was superseded in
// 1998, but rippers of the era wrote it and those files are still in people's
// collections, so they are read here and converted to 2.3 before anything is
// written — after which the ordinary ID3 path takes over.
//
// Frame bodies carry over unchanged: 2.2 and 2.3 lay out text, comments and
// lyrics identically. Only the identifier changes, and pictures, whose 2.2
// form names an image format where 2.3 wants a media type.

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"strings"
	"unicode/utf16"

	"github.com/bogem/id3v2/v2"
)

// The 2.2 frames worth carrying over, mapped to their 2.3 identifiers.
var id3v22Frames = map[string]string{
	"TT1": "TIT1", "TT2": "TIT2", "TT3": "TIT3",
	"TP1": "TPE1", "TP2": "TPE2", "TP3": "TPE3", "TP4": "TPE4",
	"TAL": "TALB", "TRK": "TRCK", "TPA": "TPOS", "TYE": "TYER",
	"TCM": "TCOM", "TCO": "TCON", "TEN": "TENC", "TBP": "TBPM",
	"TPB": "TPUB", "TLE": "TLEN", "TDA": "TDAT", "TIM": "TIME",
	"TOT": "TOAL", "TOA": "TOPE", "TOL": "TOLY", "TCR": "TCOP",
	"TCP": "TCMP", "TS2": "TSO2", "TSA": "TSOA", "TSP": "TSOP", "TST": "TSOT",
	"TXX": "TXXX", "COM": "COMM", "ULT": "USLT", "PIC": "APIC",
	"WAR": "WOAR", "WXX": "WXXX", "UFI": "UFID",
}

// id3v22Frame is one frame read out of a 2.2 tag.
type id3v22Frame struct {
	id   string
	body []byte
}

// readID3Header reports the major version and total length of the ID3v2 tag at
// the start of a file. A file with no tag reports version zero.
func readID3Header(r io.ReaderAt) (version byte, size int64, err error) {
	var head [10]byte
	if _, err := r.ReadAt(head[:], 0); err != nil {
		if errors.Is(err, io.EOF) {
			return 0, 0, nil
		}
		return 0, 0, err
	}
	if string(head[:3]) != "ID3" {
		return 0, 0, nil
	}

	// The size is four synchsafe bytes: seven bits each.
	body := int64(head[6]&0x7F)<<21 | int64(head[7]&0x7F)<<14 |
		int64(head[8]&0x7F)<<7 | int64(head[9]&0x7F)
	size = 10 + body
	if head[5]&0x10 != 0 {
		size += 10 // A footer is present.
	}
	return head[3], size, nil
}

// isID3v22 reports whether a file starts with a 2.2 tag.
func isID3v22(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()

	version, _, err := readID3Header(f)
	return err == nil && version == 2
}

// parseID3v22 reads the frames of a 2.2 tag.
func parseID3v22(path string) ([]id3v22Frame, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	version, size, err := readID3Header(f)
	if err != nil {
		return nil, err
	}
	if version != 2 || size <= 10 {
		return nil, errors.New("not an ID3v2.2 tag")
	}

	buf := make([]byte, size-10)
	if _, err := f.ReadAt(buf, 10); err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}

	var frames []id3v22Frame
	for off := 0; off+6 <= len(buf); {
		id := string(buf[off : off+3])
		// Padding: the rest of the tag is zero bytes.
		if id[0] == 0 {
			break
		}

		length := int(buf[off+3])<<16 | int(buf[off+4])<<8 | int(buf[off+5])
		start := off + 6
		if length < 0 || start+length > len(buf) {
			break // Truncated or mis-sized: keep what was read.
		}

		frames = append(frames, id3v22Frame{id: id, body: buf[start : start+length]})
		off = start + length
	}

	return frames, nil
}

// readMP3v22 reads the metadata of a file whose tag is still version 2.2.
func readMP3v22(path string) (Track, error) {
	frames, err := parseID3v22(path)
	if err != nil {
		return Track{}, wrap("reading ID3v2.2", err)
	}

	// The frame bodies of 2.2 and 2.3 have the same shape; only the names are
	// shorter, so renaming them is enough to read both the same way.
	renamed := make([]id3v22Frame, 0, len(frames))
	for _, frame := range frames {
		if id, known := id3v22Frames[frame.id]; known {
			renamed = append(renamed, id3v22Frame{id: id, body: frame.body})
		}
	}

	track := trackFromID3Frames(renamed)
	track.Duration = mp3Duration(path)
	return track, nil
}

// trackFromID3Frames reads the fields this tool cares about out of raw frames,
// named as 2.3 and 2.4 name them.
func trackFromID3Frames(frames []id3v22Frame) Track {
	var track Track

	for _, frame := range frames {
		switch frame.id {
		case frameTitle:
			track.Title = decodeTextFrame(frame.body)
		case frameArtist:
			track.Artist = decodeTextFrame(frame.body)
		case frameAlbumArtist:
			track.AlbumArtist = decodeTextFrame(frame.body)
		case frameAlbum:
			track.Album = decodeTextFrame(frame.body)
		case frameGenre:
			track.Genre = decodeTextFrame(frame.body)
		case frameTrackNo:
			track.TrackNo = leadingNumber(decodeTextFrame(frame.body))
		case frameDiscNo:
			track.DiscNo = leadingNumber(decodeTextFrame(frame.body))
		case frameYear24, frameYear23:
			if track.Year == 0 {
				track.Year = parseYear(decodeTextFrame(frame.body))
			}
		case frameCompilation:
			track.Compilation = isTrue(decodeTextFrame(frame.body))
		case frameSortAA:
			track.HasSort = decodeTextFrame(frame.body) != ""
		case frameLyrics:
			track.Lyrics = decodeLyricsFrame(frame.body)
		case frameUserText:
			description, value := decodeUserTextFrame(frame.body)
			switch {
			case matchesAny(description, albumArtistKeys) && track.AlbumArtist == "":
				track.AlbumArtist = value
			case matchesAny(description, lyricsKeys) && track.Lyrics == "":
				track.Lyrics = value
			case matchesAny(description, compilationKeys) && isTrue(value):
				track.Compilation = true
			case matchesAny(description, sortKeys) && value != "":
				track.HasSort = true
			}
		}
	}

	return track
}

// decodeTextFrame reads a text frame body: one encoding byte, then the text.
func decodeTextFrame(body []byte) string {
	if len(body) < 1 {
		return ""
	}
	return strings.TrimRight(decodeID3Text(body[0], body[1:]), "\x00")
}

// decodeUserTextFrame reads a TXX body: encoding, description, then value.
func decodeUserTextFrame(body []byte) (description, value string) {
	if len(body) < 1 {
		return "", ""
	}

	encoding := body[0]
	parts := splitID3Strings(encoding, body[1:], 2)
	if len(parts) == 0 {
		return "", ""
	}
	if len(parts) == 1 {
		return parts[0], ""
	}
	return parts[0], parts[1]
}

// decodeLyricsFrame reads a ULT body: encoding, language, descriptor, lyrics.
func decodeLyricsFrame(body []byte) string {
	if len(body) < 4 {
		return ""
	}

	encoding := body[0]
	parts := splitID3Strings(encoding, body[4:], 2) // Skip the language code.
	if len(parts) < 2 {
		return ""
	}
	return parts[1]
}

// splitID3Strings splits a run of null-terminated strings in one encoding.
func splitID3Strings(encoding byte, body []byte, want int) []string {
	terminator := 1
	if encoding == 1 || encoding == 2 {
		terminator = 2 // UTF-16 terminates with two zero bytes.
	}

	var out []string
	for len(out) < want {
		index := indexTerminator(body, terminator)
		if index < 0 || len(out) == want-1 {
			out = append(out, decodeID3Text(encoding, body))
			break
		}
		out = append(out, decodeID3Text(encoding, body[:index]))
		body = body[index+terminator:]
	}
	return out
}

// indexTerminator finds the null that ends a string of the given width.
func indexTerminator(body []byte, width int) int {
	if width == 1 {
		return bytes.IndexByte(body, 0)
	}
	for i := 0; i+1 < len(body); i += 2 {
		if body[i] == 0 && body[i+1] == 0 {
			return i
		}
	}
	return -1
}

// decodeID3Text turns a frame's bytes into a string. Version 2.2 allows only
// Latin-1 and UTF-16 with a byte order mark.
func decodeID3Text(encoding byte, body []byte) string {
	switch encoding {
	case 1, 2:
		return decodeUTF16(body)
	case 3:
		return string(body)
	default:
		// Latin-1: every byte is the code point of the same number.
		runes := make([]rune, 0, len(body))
		for _, b := range body {
			runes = append(runes, rune(b))
		}
		return string(runes)
	}
}

func decodeUTF16(body []byte) string {
	if len(body) < 2 {
		return ""
	}

	bigEndian := false
	switch {
	case body[0] == 0xFE && body[1] == 0xFF:
		bigEndian, body = true, body[2:]
	case body[0] == 0xFF && body[1] == 0xFE:
		body = body[2:]
	}

	units := make([]uint16, 0, len(body)/2)
	for i := 0; i+1 < len(body); i += 2 {
		if bigEndian {
			units = append(units, binary.BigEndian.Uint16(body[i:i+2]))
		} else {
			units = append(units, binary.LittleEndian.Uint16(body[i:i+2]))
		}
	}
	return strings.TrimRight(string(utf16.Decode(units)), "\x00")
}

// upgradeID3v22 rewrites a file's 2.2 tag as 2.3, leaving the audio untouched.
// Everything afterwards goes through the ordinary ID3 path.
func upgradeID3v22(path string) error {
	frames, err := parseID3v22(path)
	if err != nil {
		return wrap("reading ID3v2.2", err)
	}

	tag := id3v2.NewEmptyTag()
	tag.SetVersion(3)

	for _, frame := range frames {
		id, known := id3v22Frames[frame.id]
		if !known {
			continue // A frame with no 2.3 equivalent cannot be carried over.
		}

		body := frame.body
		if frame.id == "PIC" {
			if body, err = pictureToAPIC(body); err != nil {
				continue // An unreadable picture is not worth failing the file for.
			}
		}
		tag.AddFrame(id, id3v2.UnknownFrame{Body: body})
	}

	var header bytes.Buffer
	if _, err := tag.WriteTo(&header); err != nil {
		return wrap("writing ID3", err)
	}

	return replaceTag(path, header.Bytes())
}

// pictureToAPIC converts a 2.2 picture frame, whose three-letter image format
// becomes a media type in 2.3.
func pictureToAPIC(body []byte) ([]byte, error) {
	if len(body) < 5 {
		return nil, errors.New("picture frame too short")
	}

	encoding := body[0]
	format := strings.ToUpper(string(body[1:4]))
	rest := body[4:] // Picture type, description, then the image itself.

	mime := "image/jpeg"
	switch format {
	case "PNG":
		mime = "image/png"
	case "JPG", "JPE":
		mime = "image/jpeg"
	case "GIF":
		mime = "image/gif"
	case "BMP":
		mime = "image/bmp"
	default:
		mime = "image/" + strings.ToLower(format)
	}

	var out bytes.Buffer
	out.WriteByte(encoding)
	out.WriteString(mime)
	out.WriteByte(0)
	out.Write(rest)
	return out.Bytes(), nil
}

// replaceTag swaps the ID3 tag at the start of a file for a new one, writing
// beside the original and renaming, so a failure never leaves a broken track.
func replaceTag(path string, tag []byte) error {
	src, err := os.Open(path)
	if err != nil {
		return wrap("opening MP3", err)
	}
	defer src.Close()

	_, oldSize, err := readID3Header(src)
	if err != nil {
		return wrap("reading ID3", err)
	}

	info, err := src.Stat()
	if err != nil {
		return wrap("reading ID3", err)
	}

	tmp := path + ".mlm-tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return wrap("writing ID3", err)
	}

	err = writeTagAndAudio(out, src, tag, oldSize, info.Size())
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return wrap("writing ID3", err)
	}

	// Windows refuses to replace a file that is still open.
	if err := src.Close(); err != nil {
		os.Remove(tmp)
		return wrap("writing ID3", err)
	}
	return replaceFile(tmp, path)
}

func writeTagAndAudio(out io.Writer, src io.ReaderAt, tag []byte, audioAt, size int64) error {
	if _, err := out.Write(tag); err != nil {
		return err
	}
	_, err := io.Copy(out, io.NewSectionReader(src, audioAt, size-audioAt))
	return err
}
