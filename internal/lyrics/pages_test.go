package lyrics

import (
	"strings"
	"testing"
)

// The pages below are cut down from the real ones, keeping the markup that
// surrounds the words.

func TestAZLyricsStartsAtTheSong(t *testing.T) {
	page := `<div class="ringtone"></div>
<b>"Love The Way You Lie"</b><br>
<span class="feat">(feat. Rihanna)</span><br>
<br>

<div>
<!-- Usage of azlyrics.com content by any third-party lyrics provider is prohibited by our licensing agreement. Sorry about that. -->
Just gonna stand there and watch me burn<br>
Well, that's alright because I like the way it hurts<br>
<br>
Just gonna stand there and hear me cry<br>
Well, that&#39;s alright because I love the way you lie<br>
I love the way you lie
</div>
<br><br>
<div class="noprint"></div>`

	want := "Just gonna stand there and watch me burn\n" +
		"Well, that's alright because I like the way it hurts\n" +
		"\n" +
		"Just gonna stand there and hear me cry\n" +
		"Well, that's alright because I love the way you lie\n" +
		"I love the way you lie"
	if got := siteFor("www.azlyrics.com").read(page); got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
}

func TestVersesAcrossAnEmptyAdvertisement(t *testing.T) {
	page := `<nav>Home<br>Artists<br>Top</nav>
<div id="songLyricsDiv" class="lyrics-body">
<p class="lyrics-verse">Zero<br>
One<br>
Two</p>
<div class="ad-zone"><div data-fuse-pending="incontent_1"></div></div>
<p class="lyrics-verse">Three<br>
Four<br>
Five</p>
</div>
<div class="footer">Contact<br>About</div>`

	got := extractAnyLyrics(page)
	if want := "Zero\nOne\nTwo\n\nThree\nFour\nFive"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestDeclaredWordsWhenNothingIsPrinted(t *testing.T) {
	page := `<script id="__NEXT_DATA__" type="application/json">{"trackInfo":{"data":{"lyrics":{"body":"Just gonna stand there\nThat′s alright\n\nI love the way you lie","language":"en"}}}}</script>`

	got := extractAnyLyrics(page)
	if want := "Just gonna stand there\nThat's alright\n\nI love the way you lie"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestPrintedWordsOutrunADeclaredExcerpt(t *testing.T) {
	page := `<script type="application/ld+json">{"lyrics":{"@type":"CreativeWork","text":"One\nTwo"}}</script>
<div><p>One<br>Two<br>Three<br>Four<br>Five</p></div>`

	if got := extractAnyLyrics(page); !strings.HasSuffix(got, "Five") {
		t.Errorf("took the excerpt: %q", got)
	}
}

func TestAmalgamaKeepsTheOriginalOnly(t *testing.T) {
	page := `<div class="string_container"><div class="original">[Chorus: Rihanna]</div>
<div class="translate">[Припев: Rihanna]</div></div>
<div class="string_container"><div class="original">Just gonna stand there and watch me burn?</div>
<div class="translate">Будешь просто стоять и смотреть, как я горю?</div></div>
<div class="empty_container"><div class="original"><br /></div>
<div class="translate"><br /></div></div>
<div class="string_container"><div class="original">I love the way you lie</div>
<div class="translate">Мне нравится, как ты лжёшь</div></div>
<div class="string_container"><div class="original">Part 2: <a href="/songs/r/rihanna/">Rihanna</a></div></div>`

	want := "[Chorus: Rihanna]\nJust gonna stand there and watch me burn?\n\nI love the way you lie"
	if got := siteFor("amalgama-lab.com").read(page); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSiteNames(t *testing.T) {
	for host, want := range map[string]string{
		"www.musixmatch.com":   "Musixmatch",
		"m.genius.com":         "Genius",
		"www.letras.mus.br":    "Letras",
		"www.example-songs.ru": "example-songs.ru",
	} {
		if got := siteFor(host).name; got != want {
			t.Errorf("%s: got %q, want %q", host, got, want)
		}
	}
}
