#!/bin/bash

# Test script for all-snapshots functionality

echo "=== Testing Z3 All-Snapshots Feature ==="
echo

# Set test configuration
export BUCKET="test-bucket"
export FILESYSTEM="tank/test"
export S3_KEY_ID="test-key"
export S3_SECRET="test-secret"
export SNAPSHOT_PREFIX="daily"

echo "Configuration:"
echo "  Filesystem: $FILESYSTEM"
echo "  Bucket: $BUCKET"
echo "  Prefix filter: $SNAPSHOT_PREFIX"
echo

echo "1. Testing dry-run of all snapshots with prefix filter:"
./bin/z3 backup --all-snapshots --dry-run
echo

echo "2. Testing dry-run of all snapshots ignoring prefix:"
./bin/z3 backup --all-snapshots --ignore-prefix --dry-run
echo

echo "3. Testing parseable output:"
./bin/z3 backup --all-snapshots --dry-run --parseable
echo

echo "=== Test Complete ==="