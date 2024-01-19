Eris
====

Eris is a tiny RSS/Atom planet generator.

Pass it an OPML with feeds and it will go and gather the latest 250 posts from across all feed sources and output an HTML page with links to them.

Run it on a simple cron job to have your own static RSS planet.

```shell
eris feeds.opml > feeds.html
```

I love this little tool, but my god are people inconsistent with date formats on their RSS feeds.