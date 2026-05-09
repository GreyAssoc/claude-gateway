// Ported from /workspace/projects/andy/tools/gdrive.go on 2026-05-09. Adapted to a CLI surface.
//
// Differences from the original:
//   - Tool wrappers stripped; logic exposed as plain functions.
//   - DriveTrash and DriveMove added (the brief lists them explicitly even though
//     they're harness deny-listed). Original had Delete-as-trash only.
//   - ResolvePath dependency dropped — CLI callers pass absolute paths and the
//     binary's cwd is process cwd, no andy-style sandbox to worry about.
package gworkspace

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"google.golang.org/api/drive/v3"
)

// DriveLs lists files in a folder.
func DriveLs(ctx context.Context, gs *GoogleService, folderID string, maxResults int) (string, error) {
	svc, err := gs.DriveService(ctx)
	if err != nil {
		return "", err
	}
	if folderID == "" {
		folderID = "root"
	}
	if maxResults <= 0 {
		maxResults = 25
	}
	if maxResults > 100 {
		maxResults = 100
	}

	query := fmt.Sprintf("'%s' in parents and trashed = false", folderID)
	files, err := svc.Files.List().
		Q(query).
		PageSize(int64(maxResults)).
		OrderBy("modifiedTime desc").
		Fields("files(id, name, mimeType, size, modifiedTime, createdTime, parents, webViewLink)").
		Do()
	if err != nil {
		return "", fmt.Errorf("gdrive list: %w", err)
	}
	if len(files.Files) == 0 {
		return "No files found in this folder.", nil
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("**%d files:**\n\n", len(files.Files)))
	for i, f := range files.Files {
		kind := "file"
		if f.MimeType == "application/vnd.google-apps.folder" {
			kind = "folder"
		}
		b.WriteString(fmt.Sprintf("%d. [%s] **%s**\n", i+1, kind, f.Name))
		b.WriteString(fmt.Sprintf("   ID: `%s`\n", f.Id))
		if f.Size > 0 {
			b.WriteString(fmt.Sprintf("   Size: %s\n", formatFileSize(f.Size)))
		}
		if f.ModifiedTime != "" {
			b.WriteString(fmt.Sprintf("   Modified: %s\n", formatDriveTime(f.ModifiedTime)))
		}
		b.WriteString("\n")
	}
	return b.String(), nil
}

// DriveSearch searches Drive. If `query` doesn't look like Drive query syntax it's
// treated as a name-contains search.
func DriveSearch(ctx context.Context, gs *GoogleService, query string, maxResults int) (string, error) {
	svc, err := gs.DriveService(ctx)
	if err != nil {
		return "", err
	}
	if query == "" {
		return "", fmt.Errorf("query is required")
	}
	if maxResults <= 0 {
		maxResults = 20
	}
	if maxResults > 50 {
		maxResults = 50
	}

	var qParts []string
	if !strings.Contains(query, " contains ") && !strings.Contains(query, " = ") &&
		!strings.Contains(query, " in ") && !strings.Contains(query, "mimeType") {
		qParts = append(qParts, fmt.Sprintf("name contains '%s'", strings.ReplaceAll(query, "'", "\\'")))
	} else {
		qParts = append(qParts, query)
	}
	qParts = append(qParts, "trashed = false")
	fullQuery := strings.Join(qParts, " and ")

	files, err := svc.Files.List().
		Q(fullQuery).
		PageSize(int64(maxResults)).
		OrderBy("modifiedTime desc").
		Fields("files(id, name, mimeType, size, modifiedTime, webViewLink, parents)").
		Do()
	if err != nil {
		return "", fmt.Errorf("gdrive search: %w", err)
	}
	if len(files.Files) == 0 {
		return fmt.Sprintf("No files found matching '%s'.", query), nil
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("**%d results** for '%s':\n\n", len(files.Files), query))
	for i, f := range files.Files {
		b.WriteString(fmt.Sprintf("%d. **%s**\n", i+1, f.Name))
		b.WriteString(fmt.Sprintf("   ID: `%s`\n", f.Id))
		b.WriteString(fmt.Sprintf("   Type: %s\n", friendlyMimeType(f.MimeType)))
		if f.Size > 0 {
			b.WriteString(fmt.Sprintf("   Size: %s\n", formatFileSize(f.Size)))
		}
		if f.ModifiedTime != "" {
			b.WriteString(fmt.Sprintf("   Modified: %s\n", formatDriveTime(f.ModifiedTime)))
		}
		if f.WebViewLink != "" {
			b.WriteString(fmt.Sprintf("   Link: %s\n", f.WebViewLink))
		}
		b.WriteString("\n")
	}
	return b.String(), nil
}

// DriveDownload downloads a file or exports a Google-native doc to `dest`.
func DriveDownload(ctx context.Context, gs *GoogleService, fileID, dest, exportFmt string) (string, error) {
	svc, err := gs.DriveService(ctx)
	if err != nil {
		return "", err
	}
	if fileID == "" {
		return "", fmt.Errorf("file_id is required")
	}
	if exportFmt == "" {
		exportFmt = "pdf"
	}

	meta, err := svc.Files.Get(fileID).Fields("id, name, mimeType, size").Do()
	if err != nil {
		return "", fmt.Errorf("gdrive get metadata: %w", err)
	}

	isGoogleType := strings.HasPrefix(meta.MimeType, "application/vnd.google-apps.")

	var reader io.ReadCloser
	var filename string
	if isGoogleType {
		exportMime := googleExportMime(meta.MimeType, exportFmt)
		resp, err := svc.Files.Export(fileID, exportMime).Download()
		if err != nil {
			return "", fmt.Errorf("gdrive export: %w", err)
		}
		reader = resp.Body
		filename = meta.Name + "." + exportFmt
	} else {
		resp, err := svc.Files.Get(fileID).Download()
		if err != nil {
			return "", fmt.Errorf("gdrive download: %w", err)
		}
		reader = resp.Body
		filename = meta.Name
	}
	defer reader.Close()

	if dest == "" {
		dest = filename
	} else if info, err := os.Stat(dest); err == nil && info.IsDir() {
		dest = filepath.Join(dest, filename)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return "", fmt.Errorf("create directory: %w", err)
	}
	out, err := os.Create(dest)
	if err != nil {
		return "", fmt.Errorf("create file: %w", err)
	}
	defer out.Close()
	n, err := io.Copy(out, reader)
	if err != nil {
		return "", fmt.Errorf("write file: %w", err)
	}
	return fmt.Sprintf("Downloaded **%s** to `%s` (%s)", meta.Name, dest, formatFileSize(n)), nil
}

// DriveUpload uploads a local file. If `name` is empty, uses the local basename.
func DriveUpload(ctx context.Context, gs *GoogleService, localPath, parentFolderID, name string) (string, error) {
	svc, err := gs.DriveService(ctx)
	if err != nil {
		return "", err
	}
	if localPath == "" {
		return "", fmt.Errorf("path is required")
	}
	f, err := os.Open(localPath)
	if err != nil {
		return "", fmt.Errorf("open file: %w", err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("stat file: %w", err)
	}
	if name == "" {
		name = filepath.Base(localPath)
	}
	driveFile := &drive.File{Name: name}
	if parentFolderID != "" {
		driveFile.Parents = []string{parentFolderID}
	}
	created, err := svc.Files.Create(driveFile).
		Media(f).
		Fields("id, name, webViewLink, size").
		Do()
	if err != nil {
		return "", fmt.Errorf("gdrive upload: %w", err)
	}
	return fmt.Sprintf("Uploaded **%s** (%s)\nID: `%s`\nLink: %s",
		created.Name, formatFileSize(info.Size()), created.Id, created.WebViewLink), nil
}

// DriveMkdir creates a folder.
func DriveMkdir(ctx context.Context, gs *GoogleService, name, parentID string) (string, error) {
	svc, err := gs.DriveService(ctx)
	if err != nil {
		return "", err
	}
	if name == "" {
		return "", fmt.Errorf("folder name is required")
	}
	folder := &drive.File{
		Name:     name,
		MimeType: "application/vnd.google-apps.folder",
	}
	if parentID != "" {
		folder.Parents = []string{parentID}
	}
	created, err := svc.Files.Create(folder).
		Fields("id, name, webViewLink").
		Do()
	if err != nil {
		return "", fmt.Errorf("gdrive mkdir: %w", err)
	}
	return fmt.Sprintf("Folder created: **%s**\nID: `%s`\nLink: %s",
		created.Name, created.Id, created.WebViewLink), nil
}

// DriveTrash moves a file to trash. The brief flags this as deny-listed at the
// harness — the function is here so the surface is complete.
func DriveTrash(ctx context.Context, gs *GoogleService, fileID string) (string, error) {
	svc, err := gs.DriveService(ctx)
	if err != nil {
		return "", err
	}
	if fileID == "" {
		return "", fmt.Errorf("file_id is required")
	}
	meta, err := svc.Files.Get(fileID).Fields("name").Do()
	if err != nil {
		return "", fmt.Errorf("gdrive get: %w", err)
	}
	if _, err := svc.Files.Update(fileID, &drive.File{Trashed: true}).Do(); err != nil {
		return "", fmt.Errorf("gdrive trash: %w", err)
	}
	return fmt.Sprintf("Moved to trash: **%s** (`%s`)", meta.Name, fileID), nil
}

// DriveDelete permanently deletes a file. Harness deny-listed.
func DriveDelete(ctx context.Context, gs *GoogleService, fileID string) (string, error) {
	svc, err := gs.DriveService(ctx)
	if err != nil {
		return "", err
	}
	if fileID == "" {
		return "", fmt.Errorf("file_id is required")
	}
	if err := svc.Files.Delete(fileID).Do(); err != nil {
		return "", fmt.Errorf("gdrive delete: %w", err)
	}
	return fmt.Sprintf("Deleted permanently: %s", fileID), nil
}

// DriveMove moves a file to a different parent folder. Harness deny-listed.
func DriveMove(ctx context.Context, gs *GoogleService, fileID, newParentID string) (string, error) {
	svc, err := gs.DriveService(ctx)
	if err != nil {
		return "", err
	}
	if fileID == "" || newParentID == "" {
		return "", fmt.Errorf("file_id and new parent id are required")
	}
	cur, err := svc.Files.Get(fileID).Fields("parents, name").Do()
	if err != nil {
		return "", fmt.Errorf("gdrive get parents: %w", err)
	}
	updated, err := svc.Files.Update(fileID, &drive.File{}).
		AddParents(newParentID).
		RemoveParents(strings.Join(cur.Parents, ",")).
		Fields("id, name, parents").
		Do()
	if err != nil {
		return "", fmt.Errorf("gdrive move: %w", err)
	}
	return fmt.Sprintf("Moved **%s** to parent `%s`", updated.Name, newParentID), nil
}

// DriveInfo returns metadata for one file.
func DriveInfo(ctx context.Context, gs *GoogleService, fileID string) (string, error) {
	svc, err := gs.DriveService(ctx)
	if err != nil {
		return "", err
	}
	if fileID == "" {
		return "", fmt.Errorf("file_id is required")
	}
	f, err := svc.Files.Get(fileID).
		Fields("id, name, mimeType, size, createdTime, modifiedTime, owners, parents, webViewLink, sharingUser, shared, permissions").
		Do()
	if err != nil {
		return "", fmt.Errorf("gdrive info: %w", err)
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("**%s**\n", f.Name))
	b.WriteString(fmt.Sprintf("ID: `%s`\n", f.Id))
	b.WriteString(fmt.Sprintf("Type: %s\n", friendlyMimeType(f.MimeType)))
	if f.Size > 0 {
		b.WriteString(fmt.Sprintf("Size: %s\n", formatFileSize(f.Size)))
	}
	if f.CreatedTime != "" {
		b.WriteString(fmt.Sprintf("Created: %s\n", formatDriveTime(f.CreatedTime)))
	}
	if f.ModifiedTime != "" {
		b.WriteString(fmt.Sprintf("Modified: %s\n", formatDriveTime(f.ModifiedTime)))
	}
	if len(f.Owners) > 0 {
		var names []string
		for _, o := range f.Owners {
			names = append(names, o.DisplayName)
		}
		b.WriteString(fmt.Sprintf("Owner: %s\n", strings.Join(names, ", ")))
	}
	if f.WebViewLink != "" {
		b.WriteString(fmt.Sprintf("Link: %s\n", f.WebViewLink))
	}
	b.WriteString(fmt.Sprintf("Shared: %v\n", f.Shared))
	if len(f.Permissions) > 0 {
		b.WriteString("\nPermissions:\n")
		for _, p := range f.Permissions {
			email := p.EmailAddress
			if email == "" {
				email = p.Type
			}
			b.WriteString(fmt.Sprintf("  - %s: %s\n", email, p.Role))
		}
	}
	return b.String(), nil
}

// ─── Helpers ────────────────────────────────────────────────────────────────────

func formatFileSize(bytes int64) string {
	const (
		KB = 1024
		MB = KB * 1024
		GB = MB * 1024
	)
	switch {
	case bytes >= GB:
		return fmt.Sprintf("%.1f GB", float64(bytes)/float64(GB))
	case bytes >= MB:
		return fmt.Sprintf("%.1f MB", float64(bytes)/float64(MB))
	case bytes >= KB:
		return fmt.Sprintf("%.1f KB", float64(bytes)/float64(KB))
	default:
		return fmt.Sprintf("%d B", bytes)
	}
}

func formatDriveTime(rfc3339 string) string {
	t, err := time.Parse(time.RFC3339, rfc3339)
	if err != nil {
		return rfc3339
	}
	return t.Local().Format("Mon 02 Jan 2006 15:04")
}

func friendlyMimeType(mimeStr string) string {
	switch mimeStr {
	case "application/vnd.google-apps.folder":
		return "Folder"
	case "application/vnd.google-apps.spreadsheet":
		return "Google Sheets"
	case "application/vnd.google-apps.document":
		return "Google Docs"
	case "application/vnd.google-apps.presentation":
		return "Google Slides"
	case "application/vnd.google-apps.form":
		return "Google Forms"
	case "application/pdf":
		return "PDF"
	default:
		return mimeStr
	}
}

func googleExportMime(googleMime, format string) string {
	switch format {
	case "pdf":
		return "application/pdf"
	case "docx":
		return "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	case "xlsx":
		return "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	case "csv":
		return "text/csv"
	case "txt":
		return "text/plain"
	case "pptx":
		return "application/vnd.openxmlformats-officedocument.presentationml.presentation"
	default:
		return "application/pdf"
	}
}
