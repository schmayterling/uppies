package web

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"

	"uppies/internal/store"
)

// maxUploadBytes caps the upload form: two images at the size limit plus form overhead.
const maxUploadBytes = 2*store.MaxImageBytes + 64<<10

// imageTypes are the formats accepted for logos and favicons.
var imageTypes = []string{"image/png", "image/jpeg", "image/webp", "image/gif", "image/x-icon", "image/svg+xml"}

// sniffImageType returns the image type of data, or "" when it is not an
// accepted image. It looks at the bytes, never at the name or the type the
// browser claimed.
func sniffImageType(data []byte) string {
	detected := http.DetectContentType(data)
	for _, accepted := range imageTypes {
		if detected == accepted {
			return accepted
		}
	}
	// SVG is XML text, which the sniffer reports as plain text or XML.
	head := data[:min(len(data), 1024)]
	if bytes.Contains(head, []byte("<svg")) {
		return "image/svg+xml"
	}
	return ""
}

// readUpload returns the bytes and type of an uploaded image field. It
// returns no data and no problem when the field was left empty.
func readUpload(r *http.Request, field string, maxBytes int) (data []byte, contentType, problem string, err error) {
	file, _, err := r.FormFile(field)
	if errors.Is(err, http.ErrMissingFile) {
		return nil, "", "", nil
	}
	if err != nil {
		return nil, "", "", err
	}
	data, err = io.ReadAll(io.LimitReader(file, int64(maxBytes)+1))
	if err := errors.Join(err, file.Close()); err != nil {
		return nil, "", "", err
	}
	switch contentType = sniffImageType(data); {
	case len(data) == 0:
		return nil, "", "", nil
	case len(data) > maxBytes:
		return nil, "", fmt.Sprintf("the file is larger than %d KB.", maxBytes>>10), nil
	case contentType == "":
		return nil, "", "use a png, jpeg, webp, gif, ico or svg image.", nil
	}
	return data, contentType, "", nil
}

func (s *Server) handleUpdateImages(w http.ResponseWriter, r *http.Request) {
	page, err := s.settingsPage(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err := r.ParseMultipartForm(maxUploadBytes); err != nil {
		s.fail(w, r, err)
		return
	}
	type upload struct {
		data        []byte
		contentType string
	}
	uploads := make(map[string]upload)
	problems := make(map[string]string)
	for _, kind := range []string{store.ImageLogo, store.ImageFavicon} {
		data, contentType, problem, err := readUpload(r, kind, store.MaxImageBytes)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		if problem != "" {
			problems[kind] = problem
		}
		if data != nil {
			uploads[kind] = upload{data, contentType}
		}
	}
	if len(problems) > 0 {
		page.Problems = problems
		s.render(w, r, http.StatusUnprocessableEntity, "settings.html", page)
		return
	}

	nowMs := s.now().UnixMilli()
	for _, kind := range []string{store.ImageLogo, store.ImageFavicon} {
		var err error
		if up, uploaded := uploads[kind]; uploaded {
			err = s.store.SetProjectImage(r.Context(), page.Project.ID, kind, up.contentType, up.data, nowMs)
		} else if r.PostForm.Get("remove_"+kind) == "on" {
			err = s.store.DeleteProjectImage(r.Context(), page.Project.ID, kind)
		}
		if err != nil {
			s.fail(w, r, err)
			return
		}
	}
	http.Redirect(w, r, fmt.Sprintf("/admin/projects/%d/settings", page.Project.ID), http.StatusSeeOther)
}

// handleImage serves a project's logo or favicon.
func (s *Server) handleImage(w http.ResponseWriter, r *http.Request) {
	project, err := s.store.ProjectBySlug(r.Context(), r.PathValue("slug"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	contentType, data, err := s.store.ProjectImage(r.Context(), project.ID, r.PathValue("kind"))
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeImage(w, r, contentType, data)
}

// writeImage sends an uploaded image.
func (s *Server) writeImage(w http.ResponseWriter, r *http.Request, contentType string, data []byte) {
	h := w.Header()
	h.Set("Content-Type", contentType)
	h.Set("Cache-Control", "max-age=300")
	// An SVG can carry scripts. Opened on its own it would run them on this
	// site's origin, so the file is served sandboxed with everything switched off.
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
	if _, err := w.Write(data); err != nil {
		s.log.Error("write image", "path", r.URL.Path, "error", err)
	}
}
