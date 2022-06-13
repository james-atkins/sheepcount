;(function() {
    "use strict";
    var d = document;
    var w = window;
    var n = navigator;
    var url = "{{ .Url }}";

    function payload(event) {
        var p = {
            e: event,
            u: d.URL,
            r: d.referrer,
            b: 0,
            h: w.screen.height,
            w: w.screen.width,
            p: w.devicePixelRatio || 1
        };

        if (w.callPhantom || w._phantom || w.phantom) p.b = 150;
        if (w.__nightmare) p.b = 151;
        if (d.__selenium_unwrapped || d.__webdriver_evaluate || d.__driver_evaluate) p.b = 152;
        if (n.webdriver) p.b = 153;
        if (w.Cypress) p.b = 154;

        return JSON.stringify(p);
    }

    function page_view() {
        {{- if not .AllowLocalhost }}
        if (location.hostname.match(/(^localhost$|^127\.|^10\.|^172\.(1[6-9]|2[0-9]|3[0-1])\.|^192\.168\.|^0\.0\.0\.0$|^100\.)/)) {
            return;
        }
        {{- end }}
        if (location.protocol == "file:") {
            return;
        }

        var xhr = new XMLHttpRequest();
        xhr.open("POST", url, true);
        xhr.onreadystatechange = function() {
            if (xhr.readyState === XMLHttpRequest.DONE && xhr.status !== 204) {
                console.log(xhr.statusText);
            }
        };
        xhr.send(payload("l"));

        if (typeof n.sendBeacon !== "undefined") {
            d.addEventListener("visibilitychange", function() {
                if (d.visibilityState === "visible") {
                    n.sendBeacon(url, payload("v"));
                } else if (d.visibilityState === "hidden") {
                    n.sendBeacon(url, payload("h"));
                }
            });
        }
    }

    window.addEventListener("DOMContentLoaded", function() {
        if (d.visibilityState === "prerender") {
            d.addEventListener("visibilitychange", function handler() {
                if (d.visibilityState !== "visible") {
                    return;
                }
                this.removeEventListener("visibilitychange", handler);
                page_view();
            });
        } else {
            page_view();
        }
    });
})();
