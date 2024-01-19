// Copyright (c) Alisdair MacLeod <copying@alisdairmacleod.co.uk>
//
// Permission to use, copy, modify, and/or distribute this software for any
// purpose with or without fee is hereby granted.
//
// THE SOFTWARE IS PROVIDED "AS IS" AND THE AUTHOR DISCLAIMS ALL WARRANTIES WITH
// REGARD TO THIS SOFTWARE INCLUDING ALL IMPLIED WARRANTIES OF MERCHANTABILITY
// AND FITNESS. IN NO EVENT SHALL THE AUTHOR BE LIABLE FOR ANY SPECIAL, DIRECT,
// INDIRECT, OR CONSEQUENTIAL DAMAGES OR ANY DAMAGES WHATSOEVER RESULTING FROM
// LOSS OF USE, DATA OR PROFITS, WHETHER IN AN ACTION OF CONTRACT, NEGLIGENCE OR
// OTHER TORTIOUS ACTION, ARISING OUT OF OR IN CONNECTION WITH THE USE OR
// PERFORMANCE OF THIS SOFTWARE.

package main

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/html/charset"
)

const (
	// HTTP client connection timeout. 15 seconds is an arbitrary number to try
	// to limit the amount of time wasted on servers with poor connections.
	clientTimeout = 15 * time.Second
	// Maximum number of concurrent connections allowed per host. Lots of feeds
	// (especially podcasts) use the same host, and so we can get forced resets
	// if we try to connect too fast.
	connsPerHost = 20
	// Maximum number of entries to include in the HTML output.
	maxEntries = 250
)

const (
	feedTmpl = `<!doctype html>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Eris Feeds</title>
{{range .}}<p><a href="{{.Link}}">{{.EntryTitle}}</a></p>
{{end -}}`
)

type Entry struct {
	EntryTitle  string
	Link        string
	Description string
	Time        time.Time
}

type node struct {
	XMLName xml.Name
	Attrs   []xml.Attr `xml:"-"`
	Content []byte     `xml:",innerxml"`
	Nodes   []node     `xml:",any"`
}

type rss struct {
	Items []item `xml:"channel>item"`
}

type item struct {
	Title       string `xml:"title"`
	PubDate     string `xml:"pubDate"`
	Link        string `xml:"link"`
	Description string `xml:"description"`
}

type atom struct {
	Entries []entry `xml:"entry"`
}

type entry struct {
	Title   string `xml:"title"`
	Updated string `xml:"updated"`
	Link    link   `xml:"link"`
}

type link struct {
	Href string `xml:"href,attr"`
}

type opml struct {
	XMLName  xml.Name  `xml:"opml"`
	Outlines []outline `xml:"body>outline"`
}

type outline struct {
	Type     string    `xml:"type,attr"`
	Text     string    `xml:"text,attr"`
	XmlUrl   string    `xml:"xmlUrl,attr"`
	Outlines []outline `xml:"outline"`
}

func parseFeed(feed []byte) ([]Entry, error) {
	var unknownFeed node
	if err := unmarshal(feed, &unknownFeed); err != nil {
		return nil, fmt.Errorf("unmarshaling unknown feed: %w", err)
	}
	var ret []Entry
	switch strings.ToLower(unknownFeed.XMLName.Local) {
	case "feed":
		var f atom
		if err := unmarshal(feed, &f); err != nil {
			return nil, fmt.Errorf("unmarshaling atom feed: %w", err)
		}
		for _, entry := range f.Entries {
			date, err := parseDate(entry.Updated)
			switch {
			case errors.Is(err, errNoDate):
				date = time.Now()
			case err != nil:
				return nil, fmt.Errorf("parse Updated node for atom entry: %w", err)
			}
			ret = append(ret, Entry{
				EntryTitle: entry.Title,
				Link:       entry.Link.Href,
				Time:       date,
			})
		}
		return ret, nil
	case "rdf":
		fallthrough
	case "rss":
		var f rss
		if err := unmarshal(feed, &f); err != nil {
			return nil, fmt.Errorf("unmarshaling rss feed: %w", err)
		}
		for _, item := range f.Items {
			date, err := parseDate(item.PubDate)
			switch {
			case errors.Is(err, errNoDate):
				date = time.Now()
			case err != nil:
				return nil, fmt.Errorf("parse pubDate node for rss item: %w", err)
			}
			ret = append(ret, Entry{
				EntryTitle:  item.Title,
				Link:        item.Link,
				Description: item.Description,
				Time:        date,
			})
		}
		return ret, nil
	default:
		return nil, errors.New("unknown feed type")
	}
}

var dateFormats = []string{
	time.RFC822,
	time.RFC822Z,
	time.RFC1123,
	time.RFC1123Z,
	time.RFC3339,
	"02 Jan 2006 15:04:05 MST",       // RFC822 with full year and seconds
	"02 Jan 2006 15:04:05 -0700",     // RFC822Z with full year and seconds
	"2 Jan 2006 15:04:05 -0700",      // RFC822Z with full year, seconds and without padded day
	"Mon, 2 Jan 2006 15:04:05 MST",   // RFC1123 without padded day
	"Mon, 2 Jan 2006 15:04:05 -0700", // RFC1123Z without padded day
	"2006-01-02",                     // RFC3339 date only
	"2006-01-02 15:04:05",            // A common attempt at RFC3339 but with no timezone or 'T' delimiter
}

var errNoDate = errors.New("no date specified")

func parseDate(dateString string) (time.Time, error) {
	dateString = strings.TrimSpace(dateString)
	if dateString == "" {
		return time.Time{}, errNoDate
	}
	for _, format := range dateFormats {
		if t, err := time.Parse(format, dateString); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("cannot parse date string: %q", dateString)
}

func unmarshal(data []byte, v interface{}) error {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	decoder.Strict = false
	decoder.CharsetReader = charset.NewReaderLabel
	return decoder.Decode(v)
}

func parseOPML(oo []outline) []string {
	var ret []string
	for _, o := range oo {
		if o.Type == "rss" {
			ret = append(ret, o.XmlUrl)
		}
		ret = append(ret, parseOPML(o.Outlines)...)
	}
	return ret
}

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Please specify an opml file to read feeds from.")
		os.Exit(1)
	}
	log.SetOutput(os.Stderr)
	feedFile, err := os.Open(os.Args[1])
	if err != nil {
		fmt.Printf("Could not open file %q: %v\n", os.Args[1], err)
		os.Exit(1)
	}
	var OPML opml
	if err := xml.NewDecoder(feedFile).Decode(&OPML); err != nil {
		fmt.Printf("Could not parse OPML: %v\n", err)
		os.Exit(1)
	}
	feedUrls := parseOPML(OPML.Outlines)
	tmpl := template.Must(template.New("feeds").Parse(feedTmpl))
	client := &http.Client{
		Timeout: clientTimeout,
		Transport: &http.Transport{
			MaxConnsPerHost: connsPerHost,
		},
	}

	entryChan := make(chan []Entry)
	var wg sync.WaitGroup
	for _, text := range feedUrls {
		wg.Add(1)
		go func(url string) {
			defer wg.Done()
			req, err := http.NewRequest("GET", url, nil)
			if err != nil {
				log.Printf("error creating request for %q: %v\n", url, err)
				return
			}
			req.Header.Add("User-Agent", "eris (https://github.com/admacleod/eris)")
			res, err := client.Do(req)
			if err != nil {
				// Ignore HTTP errors, all they do is clog up logs when servers
				// temporarily go offline.
				return
			}
			if res.StatusCode != http.StatusOK {
				log.Printf("non-OK status code from %q: %d %s", url, res.StatusCode, res.Status)
				return
			}
			defer func() {
				if err := res.Body.Close(); err != nil {
					log.Printf("error closing request body for %q: %v\n", url, err)
				}
			}()
			rawFeed, err := io.ReadAll(res.Body)
			if err != nil {
				log.Printf("error reading feeds for %q: %v\n", url, err)
				return
			}
			parsedEntries, err := parseFeed(rawFeed)
			if err != nil {
				log.Printf("error gathering feed entries for %q: %v\n", url, err)
				return
			}
			entryChan <- parsedEntries
		}(text)
	}

	entrySet := make(map[string]Entry)
	done := make(chan struct{})
	go func() {
		for entries := range entryChan {
			for _, entry := range entries {
				entrySet[entry.Link] = entry
			}
		}
		close(done)
	}()

	wg.Wait()
	close(entryChan)
	<-done

	var entries []Entry
	for _, entry := range entrySet {
		entries = append(entries, entry)
	}

	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].Time.After(entries[j].Time)
	})

	if len(entries) > maxEntries {
		entries = entries[:maxEntries]
	}

	if err := tmpl.Execute(os.Stdout, entries); err != nil {
		log.Fatalf("error executing html template: %v\n", err)
	}
}
