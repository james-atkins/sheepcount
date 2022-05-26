package main

import (
	"net/url"
	"strings"
)

// See https://github.com/arp242/goatcounter/blob/dc6295ecec161085d667866ab1c9e2e59dc63065/hit.go#L120
func stripTrackingTags(q url.Values) {
	if len(q) == 0 {
		return
	}

	// Facebook (https://developers.facebook.com/docs/marketing-api/conversions-api/parameters/fbp-and-fbc/)
	q.Del("fbclid")

	// ProductHunt and a few others
	q.Del("ref")

	// MailChimp
	q.Del("mc_cid")
	q.Del("mc_eid")

	// Google tracking parameters
	for k := range q {
		if strings.HasPrefix(k, "utm_") {
			q.Del(k)
		}
	}

	// AdWords click ID
	q.Del("gclid")

	// Some WeChat tracking thing; see e.g:
	// https://translate.google.com/translate?sl=auto&tl=en&u=https%3A%2F%2Fsheshui.me%2Fblogs%2Fexplain-wechat-nsukey-url
	// https://translate.google.com/translate?sl=auto&tl=en&u=https%3A%2F%2Fwww.v2ex.com%2Ft%2F312163
	q.Del("nsukey")
	q.Del("isappinstalled")
	if q.Get("from") == "singlemessage" || q.Get("from") == "groupmessage" {
		q.Del("from")
	}

	// Cloudflare
	q.Del("__cf_chl_captcha_tk__")
	q.Del("__cf_chl_jschl_tk__")

	// Added by Weibo.cn (a sort of Chinese Twitter), with a random ID:
	//   /?continueFlag=4020a77be9019cf14fefc373267aa46e
	//   /?continueFlag=c397418f4346f293408b311b1bc819d4
	// Presumably a tracking thing?
	q.Del("continueFlag")
}
