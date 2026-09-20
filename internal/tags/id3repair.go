package tags

// Reading the tags of files the ordinary reader gives up on, and taking the
// obsolete tag off the end of a file.
//
// Two things in a real collection defeat a strict reader. A tag whose frames
// cannot be walked in one pass — a large picture frame the library stops
// reading half-way through leaves it looking at image data where the next
// frame header should be — makes the whole file unreadable although every
// field is still there. And a file can carry a second, older tag at the end,
// left over from a tagger that wrote both; Android's media database prefers
// that one, so a track whose artist was corrected at the front of the file
// keeps showing the old name in the player.

import (
	"bytes"
	"errors"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/bogem/id3v2/v2"
)

// parseID3v2Frames reads the frames of a 2.3 or 2.4 tag by hand, keeping
// whatever it could walk rather than failing the file.
func parseID3v2Frames(path string) (version byte, frames []id3v22Frame, err error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, nil, err
	}
	defer f.Close()

	version, size, err := readID3Header(f)
	if err != nil {
		return 0, nil, err
	}
	if version < 3 || size <= 10 {
		return version, nil, errors.New("no ID3v2.3 or 2.4 tag")
	}

	buf := make([]byte, size-10)
	if _, err := f.ReadAt(buf, 10); err != nil && !errors.Is(err, io.EOF) {
		return version, nil, err
	}

	for off := 0; off+10 <= len(buf); {
		id := string(buf[off : off+4])
		if !plausibleFrameID(id) {
			break // Padding, or the point where the tag stops making sense.
		}

		body := off + 10
		length := frameBodySize(version, buf[off+4:off+8], body, buf)
		if length < 0 {
			break
		}

		frames = append(frames, id3v22Frame{id: id, body: buf[body : body+length]})
		off = body + length
	}

	return version, frames, nil
}

// frameBodySize reads one frame's length. Version 2.4 stores it as four
// synchsafe bytes and 2.3 as a plain integer, and writers get this wrong in
// both directions — so the version's own reading is tried first and the other
// is accepted if that one lands somewhere impossible.
func frameBodySize(version byte, raw []byte, body int, buf []byte) int {
	plain := int(raw[0])<<24 | int(raw[1])<<16 | int(raw[2])<<8 | int(raw[3])
	synchsafe := int(raw[0]&0x7F)<<21 | int(raw[1]&0x7F)<<14 | int(raw[2]&0x7F)<<7 | int(raw[3]&0x7F)

	first, second := plain, synchsafe
	if version >= 4 {
		first, second = synchsafe, plain
	}

	for _, length := range []int{first, second} {
		if leadsSomewhereSensible(length, body, buf) {
			return length
		}
	}

	// Neither reading is followed by anything recognisable, which is what a
	// tag that ends in damage looks like. This frame is still whole, so it is
	// kept at its own version's reading and the walk stops after it.
	if first >= 0 && body+first <= len(buf) {
		return first
	}
	return -1
}

// leadsSomewhereSensible reports whether a frame of this length fits in the tag
// and is followed by another frame or by padding.
func leadsSomewhereSensible(length, body int, buf []byte) bool {
	if length < 0 || body+length > len(buf) {
		return false
	}

	next := body + length
	if next+10 > len(buf) {
		return true // The frame ends the tag.
	}
	return buf[next] == 0 || plausibleFrameID(string(buf[next:next+4]))
}

// plausibleFrameID reports whether four bytes look like a frame name rather
// than the middle of an image.
func plausibleFrameID(id string) bool {
	if len(id) != 4 {
		return false
	}
	for _, r := range id {
		if (r < 'A' || r > 'Z') && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}

// canParseID3 reports whether the library can walk this file's tag.
//
// The file is opened here so that it is closed again whatever the answer: the
// library keeps its own handle when it gives up on a tag, and on Windows a
// file that is still open cannot be replaced by the repaired one.
func canParseID3(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()

	_, err = id3v2.ParseReader(f, id3v2.Options{Parse: true})
	return err == nil
}

// readMP3Lenient reads what a broken tag still holds.
func readMP3Lenient(path string) (Track, error) {
	_, frames, err := parseID3v2Frames(path)
	if err != nil {
		return Track{}, err
	}
	if len(frames) == 0 {
		return Track{}, errors.New("no frames could be read")
	}

	track := trackFromID3Frames(frames)
	track.Duration = mp3Duration(path)
	return track, nil
}

// rebuildID3Tag writes the tag back in a form the library can read, so a file
// whose tag defeated the parser can still be edited. Every frame that could be
// walked is carried over as it was, artwork included.
func rebuildID3Tag(path string) error {
	version, frames, err := parseID3v2Frames(path)
	if err != nil {
		return err
	}
	if len(frames) == 0 {
		return errors.New("no frames could be read")
	}

	tag := id3v2.NewEmptyTag()
	tag.SetVersion(version)
	for _, frame := range frames {
		tag.AddFrame(frame.id, id3v2.UnknownFrame{Body: frame.body})
	}

	var header bytes.Buffer
	if _, err := tag.WriteTo(&header); err != nil {
		return wrap("writing ID3", err)
	}
	return replaceTag(path, header.Bytes())
}

/* The tag at the end of the file ------------------------------------------ */

// id3v1 is the 128 bytes some taggers still write after the audio: fixed-width
// fields, no encoding byte, and no room for a name longer than thirty
// characters.
type id3v1 struct {
	Title  string
	Artist string
	Album  string
	Year   string
	Genre  byte
	Track  int
}

// readID3v1 returns the tag at the end of a file, if there is one.
func readID3v1(path string) (id3v1, bool) {
	f, err := os.Open(path)
	if err != nil {
		return id3v1{}, false
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil || info.Size() < 128 {
		return id3v1{}, false
	}

	var raw [128]byte
	if _, err := f.ReadAt(raw[:], info.Size()-128); err != nil {
		return id3v1{}, false
	}
	if string(raw[:3]) != "TAG" {
		return id3v1{}, false
	}

	tag := id3v1{
		Title:  latin1Field(raw[3:33]),
		Artist: latin1Field(raw[33:63]),
		Album:  latin1Field(raw[63:93]),
		Year:   latin1Field(raw[93:97]),
		Genre:  raw[127],
	}
	// In version 1.1 the last two bytes of the comment hold the track number.
	if raw[125] == 0 && raw[126] != 0 {
		tag.Track = int(raw[126])
	}
	return tag, true
}

// latin1Field trims a fixed-width field. The bytes are Latin-1 by the
// standard, but anything that is already valid UTF-8 is left as it is: that is
// how a tagger writes Cyrillic here, and reading it as Latin-1 would turn it
// into nonsense.
func latin1Field(raw []byte) string {
	trimmed := bytes.TrimRight(raw, "\x00 ")
	if len(trimmed) == 0 {
		return ""
	}

	text := string(trimmed)
	if !strings.ContainsRune(text, '�') && isUTF8(trimmed) {
		return cleanText(text)
	}

	runes := make([]rune, 0, len(trimmed))
	for _, b := range trimmed {
		runes = append(runes, rune(b))
	}
	return cleanText(string(runes))
}

func isUTF8(raw []byte) bool {
	// A field of plain ASCII reads the same either way; the question only
	// matters once a byte is set high.
	high := false
	for _, b := range raw {
		if b >= 0x80 {
			high = true
			break
		}
	}
	if !high {
		return true
	}
	return utf8.Valid(raw)
}

// stripID3v1 removes the tag at the end of a file, along with the extended
// block that can sit in front of it.
//
// It is only ever removed on the way out of a write, once everything it held
// has been put into the tag at the front, because a player that reads it will
// otherwise contradict the file's real tags for good.
func stripID3v1(path string) error {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil || info.Size() < 128 {
		return err
	}

	var raw [128]byte
	if _, err := f.ReadAt(raw[:], info.Size()-128); err != nil {
		return err
	}
	if string(raw[:3]) != "TAG" {
		return nil
	}

	size := info.Size() - 128
	if size >= 227 {
		var extended [4]byte
		if _, err := f.ReadAt(extended[:], size-227); err == nil && string(extended[:]) == "TAG+" {
			size -= 227
		}
	}
	return f.Truncate(size)
}

// syncID3v1 brings the tag at the end of the file into line with the one at
// the front, and is what every write ends with.
//
// Removing it outright is not safe. Android's media database ignores an ID3v2
// tag over a few megabytes — a file with a cover that size has one — and falls
// back to this tag, so a file left without either shows no artist at all. Kept
// in step, it says the same as everything else.
//
// It can only hold Latin-1, thirty characters per field. A name it cannot hold
// is not written into it: the tag is taken off instead, because a mangled name
// is worse than a missing one.
func syncID3v1(path string, tag *id3v2.Tag) error {
	if _, ok := readID3v1(path); !ok && !tagTooBigToScan(path) {
		return nil // The file has no such tag and does not need one.
	}

	values := id3v1{
		Title:  tag.GetTextFrame(frameTitle).Text,
		Artist: tag.GetTextFrame(frameArtist).Text,
		Album:  tag.GetTextFrame(frameAlbum).Text,
		Year:   tag.GetTextFrame(frameYear24).Text,
		Track:  leadingNumber(tag.GetTextFrame(frameTrackNo).Text),
	}
	if values.Year == "" {
		values.Year = tag.GetTextFrame(frameYear23).Text
	}
	if old, ok := readID3v1(path); ok {
		values.Genre = old.Genre
	}

	for _, text := range []string{values.Title, values.Artist, values.Album, values.Year} {
		if !latin1Representable(text) {
			return stripID3v1(path)
		}
	}
	return writeID3v1(path, values)
}

// id3TagSize is how many bytes of the file its ID3v2 tag takes up.
func id3TagSize(path string) int {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()

	_, size, err := readID3Header(f)
	if err != nil {
		return 0
	}
	return int(size)
}

// tagTooBigToScan reports whether this file's ID3v2 tag is past that size. Such
// a file is not read by the phone's media database at all, so the small tag at
// the end is the only thing left naming its artist — and it is worth writing
// one for that reason alone.
func tagTooBigToScan(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()

	_, size, err := readID3Header(f)
	return err == nil && size > ScannerTagLimit
}

// latin1Representable reports whether a string survives the only alphabet the
// old tag has.
func latin1Representable(text string) bool {
	for _, r := range text {
		if r > 0xFF {
			return false
		}
	}
	return true
}

// writeID3v1 replaces the tag at the end of the file with a fresh one.
func writeID3v1(path string, values id3v1) error {
	if err := stripID3v1(path); err != nil {
		return err
	}

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		return err
	}
	defer f.Close()

	var out bytes.Buffer
	out.WriteString("TAG")
	out.Write(latin1Fixed(values.Title, 30))
	out.Write(latin1Fixed(values.Artist, 30))
	out.Write(latin1Fixed(values.Album, 30))
	out.Write(latin1Fixed(values.Year, 4))

	// Version 1.1 keeps the track number in the last two bytes of the comment.
	comment := make([]byte, 30)
	if values.Track > 0 && values.Track < 256 {
		comment[29] = byte(values.Track)
	}
	out.Write(comment)
	out.WriteByte(values.Genre)

	_, err = f.Write(out.Bytes())
	return err
}

// latin1Fixed lays a string out in a fixed-width field, one byte per
// character, padded with zeros.
func latin1Fixed(text string, width int) []byte {
	field := make([]byte, width)
	i := 0
	for _, r := range text {
		if i >= width {
			break
		}
		if r > 0xFF {
			r = '?'
		}
		field[i] = byte(r)
		i++
	}
	return field
}

// mergeID3v1 fills the fields the modern tag does not have from the one at the
// end of the file, so removing that one loses nothing.
func mergeID3v1(tag *id3v2.Tag, path string, encoding id3v2.Encoding) {
	old, ok := readID3v1(path)
	if !ok {
		return
	}

	for _, field := range []struct {
		id    string
		value string
	}{
		{frameTitle, old.Title},
		{frameArtist, old.Artist},
		{frameAlbum, old.Album},
		{frameYear24, old.Year},
	} {
		if field.value != "" && tag.GetTextFrame(field.id).Text == "" {
			setTextFrame(tag, field.id, encoding, field.value)
		}
	}

	if old.Track > 0 && tag.GetTextFrame(frameTrackNo).Text == "" {
		setTextFrame(tag, frameTrackNo, encoding, formatNumber(old.Track))
	}
}
