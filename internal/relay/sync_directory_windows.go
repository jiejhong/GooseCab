package relay

// Windows does not support File.Sync on Go's ordinary directory handles. File
// contents were already synced and the no-overwrite hard link already succeeded.
// Deliberately omit only the unsupported directory sync, not any file operation:
// publication is atomic but its directory entry is not power-loss durable.
func syncPublicationDirectory(_ string) error { return nil }
