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
