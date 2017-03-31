package sse

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Listener func(data string)

type ErrListener func(err error)

type EndListener func()

type BadContentType struct {
	ContentType string
}

func (err *BadContentType) Error() string {
	return fmt.Sprintf("bad content-type %s", err.ContentType)
}

type BadStatusCode struct {
	StatusCode int
}

func (err *BadStatusCode) Error() string {
	return fmt.Sprintf("bad status code %d", err.StatusCode)
}

type EventSource struct {
	client      *http.Client
	ctx         context.Context
	cancelFunc  context.CancelFunc
	listeners   map[string][]Listener
	muListeners sync.Mutex
	headers     map[string]string
	errListener ErrListener
	endListener EndListener
	retryWait   time.Duration
	isClosed    bool
	lastEventID string
	url         string
}

func NewEventSource(c *http.Client, urlStr string) *EventSource {
	ctx, cancelFunc := context.WithCancel(context.Background())
	return &EventSource{
		client:     c,
		ctx:        ctx,
		cancelFunc: cancelFunc,
		listeners:  map[string][]Listener{},
		headers:    map[string]string{},

		// https://www.w3.org/TR/eventsource/#concept-event-stream-reconnection-time
		// "This must initially be a user-agent-defined value, probably in the region of a few seconds."
		retryWait: 1000 * time.Millisecond,

		isClosed: false,
		url:      urlStr,
	}
}

func (s *EventSource) AddHeader(name, value string) {
	s.headers[name] = value
}

func (s *EventSource) On(event string, listener Listener) {
	s.muListeners.Lock()
	defer s.muListeners.Unlock()
	if _, ok := s.listeners[event]; !ok {
		s.listeners[event] = make([]Listener, 0)
	}
	s.listeners[event] = append(s.listeners[event], listener)
}

func (s *EventSource) emit(event string, data string) {
	if listeners, ok := s.listeners[event]; ok {
		for _, listener := range listeners {
			listener(data)
		}
	}
}

func (s *EventSource) OnError(listener ErrListener) {
	s.errListener = listener
}

func (s *EventSource) emitError(err error) { // return whether to continue or abort
	if s.errListener != nil && !s.isClosed {
		s.errListener(err)
	}
}

// EndListener is called when the EventSource is closed and there will be no more events fired from it
func (s *EventSource) OnEnd(listener EndListener) {
	s.endListener = listener
}

func (s *EventSource) emitEnd() {
	if s.endListener != nil {
		s.endListener()
	}
}

func (s *EventSource) Close() {
	s.isClosed = true
	s.cancelFunc()
}

var defaultEvent = "message"

func (s *EventSource) Open() {
	for {
		s.request()
		if !s.isClosed {
			time.Sleep(s.retryWait)
			continue
		}
		break
	}
	s.cancelFunc() // it's a good practice to call cancel at the end
	s.emitEnd()
}

func (s *EventSource) request() {
	req, err := http.NewRequest("GET", s.url, nil)
	if err != nil {
		s.emitError(err)
		return
	}
	req = req.WithContext(s.ctx)

	req.Header.Set("Accept", "text/event-stream")
	if s.lastEventID != "" {
		req.Header.Set("Last-Event-ID", s.lastEventID)
	}
	for name, value := range s.headers {
		req.Header.Set(name, value)
	}

	t := s.client.Timeout
	s.client.Timeout = 0
	resp, err := s.client.Do(req)
	s.client.Timeout = t
	if err != nil {
		s.emitError(err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		s.emitError(&BadStatusCode{StatusCode: resp.StatusCode})
		return
	}

	contentType := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(contentType, "text/event-stream") {
		s.emitError(&BadContentType{ContentType: contentType})
		return
	}

	data := ""
	event := defaultEvent

	scanner := bufio.NewScanner(resp.Body) // TODO: もしBOMがあったら無視する仕様

	for scanner.Scan() { // TODO: scanner.Scanは\r?\nをdelimiterとするが、SSEの仕様上は\r単独もあり得る

		line := scanner.Text()

		// https://www.w3.org/TR/eventsource/#event-stream-interpretation
		if line == "" {
			if data != "" {
				s.emit(event, data)
				event = defaultEvent
				data = ""
			}
			continue
		}
		split := strings.SplitN(line, ":", 2)
		field := split[0]
		value := ""
		if len(split) == 2 {
			value = strings.TrimPrefix(split[1], " ")
		}
		switch field {
		case "event":
			event = value
		case "retry":
			if n, err := strconv.Atoi(value); err != nil {
				s.retryWait = time.Duration(n) * time.Millisecond
			}
		case "id":
			s.lastEventID = value
		case "data":
			if data != "" {
				data += "\n"
			}
			data += value
		default:
			// ignore
		}
	}

	if err := scanner.Err(); err != nil {
		s.emitError(err)
	}
}
