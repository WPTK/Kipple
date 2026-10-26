package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/WPTK/kipple/internal/discover"
	"github.com/WPTK/kipple/internal/feedurl"
	"github.com/WPTK/kipple/internal/fetch"
	"github.com/WPTK/kipple/internal/sched"
	"github.com/WPTK/kipple/internal/store"
)

// ---- POST /api/feeds ----

func (s *Server) addFeed(w http.ResponseWriter, r *http.Request) {
	m, ok := readObject(w, r, "url", "folder_id", "title", "allow_private_net", "discovered_from")
	if !ok {
		return
	}
	allowPrivate := false
	if raw, present := m["allow_private_net"]; present && !isNull(raw) {
		if err := json.Unmarshal(raw, &allowPrivate); err != nil {
			writeErrorMsg(w, http.StatusBadRequest, "bad_request", "allow_private_net must be true or false")
			return
		}
	}
	rawURL, ok := rawString(m["url"])
	if !ok || strings.TrimSpace(rawURL) == "" {
		writeErrorMsg(w, http.StatusBadRequest, "invalid_url", "url is required")
		return
	}
	opts := store.SubscribeOpts{}
	if raw, present := m["folder_id"]; present && !isNull(raw) {
		id, ok := parseID(raw)
		if !ok {
			writeErrorMsg(w, http.StatusBadRequest, "bad_request", "folder_id must be a folder id")
			return
		}
		if exists, err := s.db.FolderExists(r.Context(), id); err != nil {
			s.serverError(w, "add feed", err)
			return
		} else if !exists {
			writeErrorMsg(w, http.StatusBadRequest, "folder_not_found", "no such folder")
			return
		}
		opts.FolderID = id
	}
	if raw, present := m["title"]; present && !isNull(raw) {
		t, ok := rawString(raw)
		if t, ok = titleInput(t, ok); !ok {
			writeErrorMsg(w, http.StatusBadRequest, "bad_request", "title must be text up to 200 characters")
			return
		}
		opts.Title = t
	}
	// The private-network exception is for the address the user entered and the rest of its site
	// (fetch.SameSite), never for a feed a page links on another site: that is the page's choice, not
	// the user's. discovered_from is the address entered when the dialog sends a feed picked from the
	// list a page gave (status "choose"), so the pick is held to the same rule as a single linked feed.
	enteredHost := ""
	if raw, present := m["discovered_from"]; present && !isNull(raw) {
		from, ok := rawString(raw)
		var err error
		if ok {
			_, _, enteredHost, err = store.ValidateFeedURL(from, true)
		}
		if !ok || err != nil {
			writeErrorMsg(w, http.StatusBadRequest, "bad_request", "discovered_from must be the address you entered")
			return
		}
	}
	norm, _, feedHost, err := store.ValidateFeedURL(rawURL, true)
	if err != nil {
		writeErrorMsg(w, http.StatusBadRequest, "invalid_url", err.Error())
		return
	}
	if enteredHost == "" {
		enteredHost = feedHost
	}
	privateFor := func(host string) bool { return allowPrivate && fetch.SameSite(enteredHost, host) }
	// refusePrivate refuses link, a private address the request may not reach (refused: the hop the guard
	// turned away, privateRefusal).
	refusePrivate := func(status int, link, refused string) {
		writeErrorMsg(w, status, "private_address", privateRefusal(enteredHost, link, refused, false))
	}
	if _, _, _, err := store.ValidateFeedURL(norm, privateFor(feedHost)); err != nil {
		refusePrivate(http.StatusBadRequest, norm, "") // the only refusal left once the address passed above
		return
	}
	ctx := r.Context()
	if id, found, err := s.db.FindFeedID(ctx, norm); err != nil {
		s.serverError(w, "add feed", err)
		return
	} else if found {
		s.writeExisting(w, r, id)
		return
	}

	dctx, cancel := context.WithTimeout(ctx, discoverWait)
	// Same User-Agent policy as a feed fetch (fetch.user_agent_mode, a custom UA),
	// minus the per-feed switches a new feed does not have yet.
	ua, retryUA := store.ResolveUserAgent(s.db.FetchSettings(ctx), "", false)
	if ua == "" {
		ua = s.outgoingUA()
	}
	found, err := discover.Find(dctx, fetch.ScopedTransport(s.opt.Guard, feedHost, privateFor(feedHost), false, false), ua, retryUA, norm, privateFor(feedHost))
	cancel()
	if err != nil {
		code, msg := discoveryError(err)
		if code == "private_address" {
			refusePrivate(http.StatusUnprocessableEntity, norm, refusedAt(err, norm))
			return
		}
		writeErrorMsg(w, http.StatusUnprocessableEntity, code, msg)
		return
	}
	if len(found.Candidates) > 1 {
		writeJSON(w, http.StatusOK, map[string]any{"status": "choose", "candidates": found.Candidates})
		return
	}
	target := found.Candidates[0].URL
	if id, exists, err := s.db.FindFeedID(ctx, target); err != nil {
		s.serverError(w, "add feed", err)
		return
	} else if exists {
		s.writeExisting(w, r, id)
		return
	}
	_, _, targetHost, err := store.ValidateFeedURL(target, true)
	if err != nil {
		s.serverError(w, "add feed", err) // discovery returns only addresses that passed this check
		return
	}
	if _, _, _, err := store.ValidateFeedURL(target, privateFor(targetHost)); err != nil {
		refusePrivate(http.StatusUnprocessableEntity, target, "") // a linked literal private address the grant does not reach
		return
	}
	// The feed may answer through a redirect to one the user already has: that is checked on the
	// address that answered, not the one typed or linked, before anything is created. The probe of a
	// linked feed runs with what the saved feed may reach, so it also finds a linked name the guard
	// refuses (one on another site that resolves to a private address).
	owner, refused, err := s.redirectOwner(ctx, found, ua, retryUA, func(h string) (http.RoundTripper, bool) {
		priv := privateFor(h)
		return fetch.ScopedTransport(s.opt.Guard, h, priv, false, false), priv
	})
	switch {
	case err != nil:
		s.serverError(w, "add feed", err)
		return
	case refused != "":
		refusePrivate(http.StatusUnprocessableEntity, target, refused)
		return
	case owner != 0:
		s.writeFeedExists(w, r, owner, redirectTail(found.IsFeed))
		return
	}

	opts.URL, opts.AllowPrivateNet = target, privateFor(targetHost)
	res, err := s.db.Subscribe(ctx, opts)
	var bad *store.InvalidURLError
	switch {
	case errors.As(err, &bad):
		writeErrorMsg(w, http.StatusBadRequest, "invalid_url", bad.Reason)
		return
	case errors.Is(err, store.ErrFolderNotFound): // deleted since the check above
		writeErrorMsg(w, http.StatusBadRequest, "folder_not_found", "no such folder")
		return
	case err != nil:
		s.serverError(w, "add feed", err)
		return
	case res.Existed:
		s.writeExisting(w, r, res.FeedID)
		return
	}
	s.publishFeedChanged(res.FeedID)
	ch, err := s.opt.Sched.Submit(sched.Priority{FeedID: res.FeedID, Full: true, Trigger: fetch.TriggerSubscribe})
	fo := fetchOutcome{Pending: true}
	var merged int64
	if err != nil {
		s.log.Warn("api: add feed: submit first fetch", "err", err)
		s.opt.Sched.Wake() // the feed is due; the tick picks it up
	} else if rep, got, werr := s.awaitReply(r, ch, addWait); werr == nil && got && rep.Err == nil {
		fo, merged = outcomeOf(rep), rep.MergedInto
	}
	fd, ok, err := s.db.FeedDetail(ctx, res.FeedID, s.statusEnv())
	if err == nil && !ok && merged != 0 {
		// The page led, one step further, to a feed the user already has, and the first fetch removed
		// the new feed as its duplicate: the same answer as when that is known before creating it.
		s.writeFeedExists(w, r, merged, "The address you entered leads to it. A title or folder you chose (other than the default folder) was applied to it.")
		return
	}
	if err == nil && !ok {
		// Gone with no word from the fetch: it was removed while it was being added (the first fetch
		// merged it into a feed you have after this request stopped waiting, or it was deleted). That
		// is an answer, not a fault.
		writeErrorMsg(w, http.StatusConflict, "feed_gone", "That feed was removed while it was being added, most likely "+
			"because you already have it, in which case a title or folder you chose (other than the default folder) was applied to the feed you have. "+
			"Look through your feeds, and add it again if it is not there.")
		return
	}
	if err != nil {
		s.serverError(w, "add feed", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "feed": fd, "fetch": fo})
}

// msgPrivateAddress explains a private address refused by the add dialog and how to allow it.
const msgPrivateAddress = "That address is on a private network (this computer or your local network). Kipple does not " +
	"fetch from private addresses unless you allow it for the feed. If this feed is on your own network, tick " +
	"\"Allow addresses on my own network\" and add it again."

// privateRefusal explains why target may not be reached: refused is the address the guard turned
// away (target itself, or a hop target redirects to; "" means target), enteredHost the host the
// switch covers (with its site), and edit picks the feed editor's wording, where the option is
// under Unsafe options.
func privateRefusal(enteredHost, target, refused string, edit bool) string {
	turnOn, again := `tick "Allow addresses on my own network"`, " and add it again."
	if edit {
		turnOn, again = turnOn+" under Unsafe options below", " and save again."
	}
	th := urlHost(target)
	switch {
	case refused != "" && urlHost(refused) != th:
		return "That address (" + target + ") redirects to a private address (" + refused + "). \"Allow addresses on my own " +
			"network\" covers only the site of the address you enter, not one it redirects to. If the feed is there, " +
			"enter that address, " + turnOn + again
	case !fetch.SameSite(enteredHost, th):
		return "The page links its feed on another site, at a private address (" + target + "). \"Allow addresses on my own " +
			"network\" covers only the site of the address you enter. To use that feed, enter its own address, " + turnOn + again
	case edit:
		return msgPrivateAddressEdit
	}
	return msgPrivateAddress
}

// urlHost is the lowercase host of a URL, "" when it has none.
func urlHost(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

// refusedAt is the address a refused request was for: the redirect hop the guard turned away, which
// http.Client names in its *url.Error, or fallback.
func refusedAt(err error, fallback string) string {
	var ue *url.Error
	if errors.As(err, &ue) && ue.URL != "" {
		return ue.URL
	}
	return fallback
}

// linkedHost is the host of the one feed a page links (found has exactly one candidate).
func linkedHost(found discover.Result) string {
	h, _ := feedurl.Host(found.Candidates[0].URL)
	return h
}

// msgPrivateAddressEdit is the same refusal for the feed editor, where the option is under Unsafe options.
const msgPrivateAddressEdit = "That address is on a private network (this computer or your local network). Kipple does not " +
	"fetch from private addresses unless you allow it for the feed. If this feed is on your own network, tick " +
	"\"Allow addresses on my own network\" under Unsafe options below and save again."

// discoveryError maps a failed discovery to the add dialog's error code and a plain-language message.
func discoveryError(err error) (code, msg string) {
	var se *discover.StatusError
	switch {
	case errors.Is(err, discover.ErrNoFeed):
		return "no_feed", "Kipple found a web page at that address, but the page does not link to a feed. " +
			"Look on the site for its feed address (often /feed or /rss.xml)."
	case errors.Is(err, discover.ErrNotFeed):
		return "not_feed", "That address does not answer with a feed or a web page. Check the address."
	case errors.Is(err, discover.ErrTooLarge):
		return "not_feed", "That address answered with something too large to be a feed (" + err.Error() + ")."
	case errors.As(err, &se):
		return "unreachable", "The site answered, but with an error: HTTP " + strconv.Itoa(se.Code) + " " + http.StatusText(se.Code) +
			". Check the address, or try again later."
	}
	if errors.Is(err, fetch.ErrRedirectRefused) {
		return "unreachable", "That address redirects somewhere Kipple does not follow (a move from https to http, or to something other than a web address)."
	}
	switch class, detail := fetch.Classify(err); class {
	case fetch.ClassSSRF:
		return "private_address", msgPrivateAddress
	case fetch.ClassTimeout:
		return "timeout", "The site took too long to answer. Try again later."
	case fetch.ClassDNS:
		return "unreachable", "Kipple could not find that site: its name does not resolve. Check the spelling of the address."
	case fetch.ClassTLS:
		return "unreachable", "Kipple could not make a secure connection to that site (" + detail + ")."
	case fetch.ClassRedirectLoop:
		return "unreachable", "That address redirects too many times."
	default:
		return "unreachable", "Kipple could not reach that site (" + detail + ")."
	}
}

// writeFeedExists refuses an address that leads to a feed the user already has, naming that feed. tail
// is the sentence about how the address led there (redirectTail).
func (s *Server) writeFeedExists(w http.ResponseWriter, r *http.Request, id int64, tail string) {
	writeJSON(w, http.StatusConflict, map[string]any{"error": "feed_exists", "feed_id": idStr(id),
		"message": s.existingFeedMessage(r.Context(), id, tail)})
}

// redirectTail says how an address led to a feed the user already has, through a redirect: typed is true
// when the address the user entered is itself the feed that redirects, false when it is a page that links
// a feed that does.
func redirectTail(typed bool) string {
	if typed {
		return "The address you entered redirects to it."
	}
	return "The page you entered links a feed that redirects to it."
}

// redirectOwner returns the feed the user already has at the address that answered for the feed found
// describes, or 0. For a typed feed that is its own final address; for a page that links one feed it is
// where that feed answers, probed here with the transport and redirect policy its first fetch will use
// (scope builds it for the linked feed's host, with whether that host may be a private address: what the
// saved feed may reach there, which differs from the page's when the link is on another site). A probe
// that fails leaves it unknown, and the fetch that follows reports the problem, except a private address
// the saved feed may not reach (a literal one, a name the guard refuses, or a redirect hop to one): refused
// is that address, so the caller can say so instead of saving a feed that cannot be fetched. Add and the feed editor both
// ask this, so a duplicate is refused the same way in both.
func (s *Server) redirectOwner(ctx context.Context, found discover.Result, ua, retryUA string,
	scope func(host string) (http.RoundTripper, bool)) (owner int64, refused string, err error) {
	final := found.Final
	if !found.IsFeed {
		if len(found.Candidates) != 1 {
			return 0, "", nil
		}
		target := found.Candidates[0].URL
		if _, _, h, err := store.ValidateFeedURL(target, true); err == nil {
			rt, private := scope(h)
			if _, _, _, err := store.ValidateFeedURL(target, private); err != nil {
				return 0, target, nil
			}
			pctx, cancel := context.WithTimeout(ctx, discoverWait)
			defer cancel()
			probe, perr := discover.Find(pctx, rt, ua, retryUA, target, private)
			if perr == nil {
				final = probe.Final
			} else if class, _ := fetch.Classify(perr); class == fetch.ClassSSRF {
				return 0, refusedAt(perr, target), nil
			}
		}
	}
	if final == "" {
		return 0, "", nil
	}
	id, exists, err := s.db.FindFeedID(ctx, final)
	if err != nil || !exists {
		return 0, "", err
	}
	return id, "", nil
}

// existingFeedMessage says which feed the user already has: its title and, when it is in a folder, the
// folder. tail is a sentence about the address that led there.
func (s *Server) existingFeedMessage(ctx context.Context, id int64, tail string) string {
	title, folder, err := s.db.FeedLabel(ctx, id)
	if err != nil {
		return "You already have this feed. " + tail
	}
	where := ""
	if folder != "" {
		where = " in the folder " + folder
	}
	return "You already have this feed: " + title + where + ". " + tail
}

func (s *Server) writeExisting(w http.ResponseWriter, r *http.Request, id int64) {
	fd, found, err := s.db.FeedDetail(r.Context(), id, s.statusEnv())
	if err != nil || !found {
		if err == nil {
			err = store.ErrFeedNotFound
		}
		s.serverError(w, "add feed", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "exists", "feed": fd})
}
