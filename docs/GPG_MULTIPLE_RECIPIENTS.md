# GPG Multiple Recipients Support

Z3 now supports encrypting backups with multiple GPG recipients, allowing multiple users or keys to decrypt the same backup.

## Configuration

### Using Configuration File

In your `z3.conf` file, you can specify multiple recipients in two ways:

```ini
[main]
# New format - multiple recipients (recommended)
GPG_RECIPIENTS=alice@example.com,bob@example.com,charlie@example.com

# Old format - single recipient (still supported)
GPG_RECIPIENT=alice@example.com
```

### Using Environment Variables

```bash
# Multiple recipients
export GPG_RECIPIENTS="alice@example.com,bob@example.com,charlie@example.com"

# Single recipient (backwards compatible)
export GPG_RECIPIENT="alice@example.com"
```

### Using Command Line

```bash
# Single recipient
z3 backup --gpg-recipient alice@example.com

# Multiple recipients (comma-separated)
z3 backup --gpg-recipient "alice@example.com,bob@example.com,charlie@example.com"

# With compression
z3 backup --compressor gpg --gpg-recipient "alice@example.com,bob@example.com"
```

## How It Works

When multiple recipients are specified:

1. GPG encrypts the data so that ANY of the recipients can decrypt it
2. Each recipient's public key is used during encryption
3. The backup can be decrypted by any recipient using their private key
4. S3 metadata stores both formats for compatibility:
   - `gpg_recipient`: First recipient only (for old Z3 versions)
   - `gpg_recipients`: All recipients (comma-separated)

## Examples

### Backup with Multiple Recipients

```bash
# Configure recipients
export GPG_RECIPIENTS="backup@company.com,admin@company.com,disaster-recovery@company.com"

# Create encrypted backup
z3 backup --compressor pigz4,gpg

# The backup can now be decrypted by any of the three recipients
```

### Team Backup Scenario

```bash
# Development team backup - any team member can restore
z3 backup --gpg-recipient "alice@team.com,bob@team.com,charlie@team.com,dave@team.com"

# Operations team backup - separate set of recipients
z3 backup --filesystem tank/ops --gpg-recipient "ops@company.com,oncall@company.com"
```

### Migration from Single to Multiple Recipients

If you have existing backups with a single recipient and want to add more:

1. New backups will use multiple recipients
2. Old backups can still be restored (backwards compatible)
3. Consider re-encrypting critical old backups with new recipients

```bash
# Old backups used single recipient
GPG_RECIPIENT=oldkey@company.com

# New backups use multiple recipients
GPG_RECIPIENTS=oldkey@company.com,newkey@company.com,backup@company.com
```

## Backwards Compatibility

The implementation maintains full backwards compatibility:

1. **Old Z3 Python version** can read backups created with multiple recipients
   - It will see only the first recipient in metadata
   - It can still decrypt if it has any recipient's private key

2. **Configuration files** support both formats
   - If both are specified, `GPG_RECIPIENTS` takes precedence
   - Single recipient in `GPG_RECIPIENT` is automatically used

3. **S3 Metadata** includes both fields
   - `gpg_recipient`: First recipient (for compatibility)
   - `gpg_recipients`: All recipients (new format)

## Requirements

- GPG must be installed (`gpg` command available)
- Public keys for all recipients must be in your GPG keyring
- At least one recipient's private key needed for restoration

## Troubleshooting

### Missing Public Keys

If you see errors about missing keys:

```bash
# Import a recipient's public key
gpg --import recipient-public-key.asc

# List available keys
gpg --list-keys
```

### Verification

To verify which recipients can decrypt a backup:

```bash
# Check backup metadata
z3 status --verbose

# Look for gpg_recipients in the metadata output
```

## Security Considerations

1. **Key Management**: Ensure all recipient public keys are trusted
2. **Access Control**: Each recipient can decrypt the entire backup
3. **Key Rotation**: Add new recipients to new backups, keep old key for restoration
4. **Audit Trail**: S3 metadata shows all authorized recipients

## Best Practices

1. **Use descriptive key identities**: `backup-2024@company.com` instead of just `backup@company.com`
2. **Document your recipients**: Maintain a list of who has access to what
3. **Regular key audits**: Remove recipients who no longer need access
4. **Test restoration**: Verify each recipient can actually decrypt backups
5. **Separate by purpose**: Use different recipient sets for different datasets

```bash
# Production data - limited recipients
z3 backup --filesystem tank/prod --gpg-recipient "prod-backup@company.com,cto@company.com"

# Development data - broader access
z3 backup --filesystem tank/dev --gpg-recipient "dev-team@company.com,ops-team@company.com"
```