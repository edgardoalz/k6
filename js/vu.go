package js

import (
	"context"
	"errors"
	log "github.com/Sirupsen/logrus"
	"github.com/loadimpact/speedboat/proto/httpwrap"
	"github.com/loadimpact/speedboat/stats"
	"github.com/robertkrimen/otto"
	"net/http"
	neturl "net/url"
	"strings"
	"time"
)

var (
	mRequests = stats.Stat{Name: "requests", Type: stats.HistogramType, Intent: stats.TimeIntent}
	mErrors   = stats.Stat{Name: "errors", Type: stats.CounterType}

	ErrTooManyRedirects = errors.New("too many redirects")

	errInternalHandleRedirect = errors.New("[internal] handle redirect")
)

type HTTPParams struct {
	Follow  bool
	Quiet   bool
	Headers map[string]string
}

type HTTPResponse struct {
	Status  int
	Headers map[string]string
	Body    string
}

func (res HTTPResponse) ToValue(vm *otto.Otto) (otto.Value, error) {
	obj, err := Make(vm, "HTTPResponse")
	if err != nil {
		return otto.UndefinedValue(), err
	}

	obj.Set("status", res.Status)
	obj.Set("headers", res.Headers)
	obj.Set("body", res.Body)

	return vm.ToValue(obj)
}

type stringReadCloser struct {
	*strings.Reader
}

func (stringReadCloser) Close() error { return nil }

func (u *VU) HTTPRequest(method, url, body string, params HTTPParams, redirects int) (HTTPResponse, error) {
	log.WithFields(log.Fields{"method": method, "url": url, "body": body, "params": params}).Debug("Request")

	parsedURL, err := neturl.Parse(url)
	if err != nil {
		return HTTPResponse{}, err
	}

	req := http.Request{
		Method: method,
		URL:    parsedURL,
		Header: make(http.Header),
	}

	if method == "GET" || method == "HEAD" {
		req.URL.RawQuery = body
	} else {
		req.Body = stringReadCloser{strings.NewReader(body)}
		req.ContentLength = int64(len(body))
	}

	for key, value := range params.Headers {
		req.Header[key] = []string{value}
	}

	resp, respBody, sample, err := httpwrap.Do(context.Background(), &u.Client, &req, httpwrap.Params{TakeSample: !params.Quiet, KeepBody: true})

	switch e := err.(type) {
	case nil:
		if !params.Quiet {
			sample.Stat = &mRequests
			u.Collector.Add(sample)
		}
	case *neturl.Error:
		if e.Err != errInternalHandleRedirect {
			if !params.Quiet {
				u.Collector.Add(stats.Sample{
					Stat: &mErrors,
					Tags: stats.Tags{
						"method": req.Method,
						"url":    req.URL.String(),
						"error":  err.Error(),
					},
					Values: stats.Value(1),
				})
			}
			return HTTPResponse{}, err
		}

		if !params.Follow {
			break
		}

		if redirects >= u.FollowDepth {
			return HTTPResponse{}, ErrTooManyRedirects
		}

		redirectURL := resolveRedirect(url, resp.Header.Get("Location"))
		redirectMethod := method
		redirectBody := ""
		if resp.StatusCode == 301 || resp.StatusCode == 302 || resp.StatusCode == 303 {
			redirectMethod = "GET"
			if redirectMethod != method {
				redirectBody = ""
			}
		}

		return u.HTTPRequest(redirectMethod, redirectURL, redirectBody, params, redirects+1)
	default:
		if !params.Quiet {
			u.Collector.Add(stats.Sample{
				Stat: &mErrors,
				Tags: stats.Tags{
					"method": req.Method,
					"url":    req.URL.String(),
					"error":  err.Error(),
				},
				Values: stats.Value(1),
			})
		}
		return HTTPResponse{}, err
	}

	headers := make(map[string]string)
	for key, vals := range resp.Header {
		headers[key] = vals[0]
	}

	return HTTPResponse{
		Status:  resp.StatusCode,
		Headers: headers,
		Body:    string(respBody),
	}, nil
}

func (u *VU) Sleep(t float64) {
	time.Sleep(time.Duration(t * float64(time.Second)))
}

func (u *VU) Log(level, msg string, fields map[string]interface{}) {
	e := u.Runner.logger.WithFields(log.Fields(fields))

	switch level {
	case "debug":
		e.Debug(msg)
	case "info":
		e.Info(msg)
	case "warn":
		e.Warn(msg)
	case "error":
		e.Error(msg)
	}
}
