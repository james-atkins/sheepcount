# SheepCount

SheepCount is a simple web analytics software. It aims to be a privacy-friendly, self-hosted, standalone alternative to Google Analytics. It is written in Go.

## What data is collected?

SheepCount collects a user's browser and operating system version, display size, language and location. It does not collect personally identifiable information such as IP addresses.

## How is this data stored?

SheepCount uses [SQLite](https://www.sqlite.org/).

## Is there a nice web interface?

No. At the moment you have to use SQL.

## Should you use it?

Probably not. I suggest alternatives such as [GoatCounter](http://goatcounter.com/).

## How is SheepCount "privacy-friendly"?

SheepCount does not use cookies to identify users and does not store personally identifiable information such as IP addresses.

SheepCount uses a privacy-friendly method of hashes and rotating random salts to identify users.
This is similar to the method used by [Fathom](https://usefathom.com/data) and [GoatCounter](https://github.com/arp242/goatcounter/blob/master/docs/sessions.markdown).
SheepCount creates a BLAKE2b-256 hash of the browser's user agent and IP address together with a random salt, which is rotated every 12 hours.
BLAKE2b-256 is a one-way function which is practically infeasible to invert or reverse - once the salt has changed, even if the same user visits the website again from the same IP address, it is not possible to link them to existing browser session.
