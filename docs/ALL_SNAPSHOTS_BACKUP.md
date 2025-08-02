# All Snapshots Backup Feature

The Z3 Go version now supports backing up all snapshots for a filesystem in a single operation.

## Overview

This feature allows you to:
- Backup all snapshots for a filesystem at once
- Filter by snapshot prefix or ignore the prefix
- Track progress for multi-snapshot operations
- Handle errors gracefully with detailed reporting

## Usage

### Basic Usage - Backup All Snapshots

```bash
# Backup all snapshots matching the configured prefix
z3 backup --all-snapshots

# Example output:
# Found 5 snapshots to backup:
#   [1/5] zfs-auto-snap:daily-2024-01-01
#   [2/5] zfs-auto-snap:daily-2024-01-02
#   [3/5] zfs-auto-snap:daily-2024-01-03
#   [4/5] zfs-auto-snap:daily-2024-01-04
#   [5/5] zfs-auto-snap:daily-2024-01-05
```

### Ignore Prefix Filter

```bash
# Backup ALL snapshots regardless of prefix
z3 backup --all-snapshots --ignore-prefix

# This backs up every snapshot:
# - zfs-auto-snap:daily-*
# - zfs-auto-snap:weekly-*
# - manual-backup-*
# - test-snapshot-*
```

### Dry Run Mode

```bash
# See what would be backed up without making changes
z3 backup --all-snapshots --dry-run

# Combine with ignore-prefix
z3 backup --all-snapshots --ignore-prefix --dry-run
```

### Machine-Readable Output

```bash
# Get parseable output for scripting
z3 backup --all-snapshots --parseable

# Output format: snapshot_name\x00size
# tank/data@daily-2024-01-01 1234567890
# tank/data@daily-2024-01-02 2345678901
```

## How It Works

1. **Discovery Phase**
   - Lists all local snapshots for the filesystem
   - Applies prefix filter (unless --ignore-prefix)
   - Lists all remote snapshots in S3

2. **Comparison Phase**
   - Compares local vs remote snapshots
   - Identifies snapshots that need backup
   - Shows summary of what will be backed up

3. **Backup Phase**
   - Backs up each snapshot individually
   - Tracks progress with counter [N/M]
   - Handles incremental chains automatically
   - Continues on error (doesn't stop at first failure)

4. **Summary Phase**
   - Reports total snapshots processed
   - Shows success/failure counts
   - Displays total size and compression ratios
   - Reports total duration

## Features

### Smart Incremental Handling

When backing up multiple snapshots, Z3 automatically:
- Detects parent-child relationships
- Performs full backups when needed
- Uses incremental backups when possible
- Maintains snapshot chains

### Error Handling

If a snapshot fails to backup:
- Error is logged but doesn't stop the process
- Other snapshots continue to be backed up
- Final summary shows which snapshots failed
- Exit code indicates if any failures occurred

### Progress Tracking

For each snapshot:
```
=== Backing up snapshot 3/10: tank/data@daily-2024-01-03 ===
✓ Successfully backed up tank/data@daily-2024-01-03
  Size: 1.2 GB → 456 MB (38.0%) (incremental)
```

Final summary:
```
=== Backup Summary ===
Total snapshots processed: 10
  Successful: 9
  Failed: 1
Total size: 12.3 GB → 4.5 GB (36.6%)
Total duration: 5m32s
```

## Use Cases

### 1. Initial Backup of Existing System

```bash
# System has many existing snapshots, backup them all
z3 backup --all-snapshots --ignore-prefix
```

### 2. Regular Maintenance

```bash
# Backup all daily snapshots that aren't already in S3
z3 backup --all-snapshots
```

### 3. Migration from Python Z3

```bash
# Ensure all snapshots are backed up during migration
z3 backup --all-snapshots --dry-run  # Check first
z3 backup --all-snapshots             # Then backup
```

### 4. Automated Backup Script

```bash
#!/bin/bash
# Backup all snapshots and check for errors

if z3 backup --all-snapshots --parseable > backup.log; then
    echo "All snapshots backed up successfully"
else
    echo "Some snapshots failed - check backup.log"
    exit 1
fi
```

## Configuration

The feature respects all existing configuration:

```ini
[main]
FILESYSTEM=tank/data
SNAPSHOT_PREFIX=zfs-auto-snap:daily
COMPRESSOR=pigz4
# ... other settings
```

### Per-Filesystem Prefix

```ini
[fs:tank/data]
SNAPSHOT_PREFIX=daily-

[fs:tank/home]
SNAPSHOT_PREFIX=hourly-
```

## Comparison with Python Z3

| Feature | Python Z3 | Go Z3 |
|---------|-----------|-------|
| Single snapshot backup | ✓ | ✓ |
| Latest snapshot backup | ✓ | ✓ |
| All snapshots backup | ✗ | ✓ |
| Ignore prefix filter | ✗ | ✓ |
| Progress tracking | ✓ | ✓✓ (enhanced) |
| Error continuation | ✗ | ✓ |

## Performance Considerations

- Each snapshot is backed up sequentially (not parallel)
- This ensures consistent S3 bandwidth usage
- Prevents overwhelming the system with concurrent ZFS sends
- Future versions may add controlled parallelism

## Best Practices

1. **Test with dry-run first**
   ```bash
   z3 backup --all-snapshots --dry-run
   ```

2. **Monitor first full run**
   - Initial backup of many snapshots can take time
   - Watch for any permission or configuration issues

3. **Use prefix filtering wisely**
   - Default: Only backs up snapshots matching prefix
   - Use --ignore-prefix carefully (may include test snapshots)

4. **Regular incremental backups**
   - After initial backup, regular runs are fast
   - Only new snapshots need backing up

5. **Check the summary**
   - Always review the backup summary
   - Investigate any failed snapshots