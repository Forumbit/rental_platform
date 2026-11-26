#!/bin/sh
# PostgreSQL backups to S3-compatible storage.
#
#   backup.sh run              daily backup at BACKUP_HOUR_UTC, forever
#   backup.sh backup           one backup now
#   backup.sh list             list stored backups, oldest first
#   backup.sh restore [name]   restore the latest (or the named) backup
#
# Uses the same DB_* and S3_* variables as the API, plus BACKUP_BUCKET.
set -eu

: "${DB_USER:?}" "${DB_PASSWORD:?}" "${DB_NAME:?}"
: "${S3_ENDPOINT:?}" "${S3_ACCESS_KEY:?}" "${S3_SECRET_KEY:?}" "${BACKUP_BUCKET:?}"

PREFIX="${BACKUP_PREFIX:-postgres}"
KEEP="${BACKUP_KEEP:-14}"
HOUR="${BACKUP_HOUR_UTC:-0}"

export PGHOST="${DB_HOST:-postgres}" PGUSER="$DB_USER" PGPASSWORD="$DB_PASSWORD" PGDATABASE="$DB_NAME"
export AWS_ACCESS_KEY_ID="$S3_ACCESS_KEY" AWS_SECRET_ACCESS_KEY="$S3_SECRET_KEY"
export AWS_DEFAULT_REGION="${S3_REGION:-us-east-1}"
# AWS CLI v2 sends new checksum headers that many S3-compatible services reject.
export AWS_REQUEST_CHECKSUM_CALCULATION=when_required AWS_RESPONSE_CHECKSUM_VALIDATION=when_required

scheme=https
[ "${S3_USE_SSL:-true}" = "false" ] && scheme=http

log() { echo "$(date -u +%Y-%m-%dT%H:%M:%SZ) $*"; }

s3() { aws --endpoint-url "$scheme://$S3_ENDPOINT" s3 "$@"; }
s3api() { aws --endpoint-url "$scheme://$S3_ENDPOINT" s3api "$@"; }

# Some S3-compatible servers accept uploads into a missing bucket but cannot
# read them back, so the bucket must exist before the first upload.
ensure_bucket() {
	s3api head-bucket --bucket "$BACKUP_BUCKET" >/dev/null 2>&1 && return 0
	log "creating bucket $BACKUP_BUCKET"
	s3 mb "s3://$BACKUP_BUCKET" >/dev/null
}

list() {
	s3 ls "s3://$BACKUP_BUCKET/$PREFIX/" | awk '{print $4}' | grep '\.dump$' | sort || true
}

prune() {
	total=$(list | wc -l)
	excess=$((total - KEEP))
	[ "$excess" -gt 0 ] || return 0
	list | head -n "$excess" | while read -r name; do
		s3 rm --only-show-errors "s3://$BACKUP_BUCKET/$PREFIX/$name"
		log "deleted old backup $name"
	done
}

backup() {
	name="$(date -u +%Y-%m-%dT%H-%M-%SZ).dump"
	file="/tmp/$name"
	ensure_bucket
	pg_dump --format=custom --file="$file"
	size=$(wc -c <"$file")
	s3 cp --only-show-errors "$file" "s3://$BACKUP_BUCKET/$PREFIX/$name"
	rm -f "$file"
	# A backup counts only if it can be read back with the expected size.
	stored=$(s3api head-object --bucket "$BACKUP_BUCKET" --key "$PREFIX/$name" \
		--query ContentLength --output text 2>/dev/null || echo 0)
	if [ "$stored" != "$size" ]; then
		log "backup verification failed: $PREFIX/$name stored $stored of $size bytes"
		return 1
	fi
	log "backup uploaded: $PREFIX/$name ($BACKUP_BUCKET, $size bytes)"
	prune
}

restore() {
	name="${1:-$(list | tail -n 1)}"
	if [ -z "$name" ]; then
		log "no backups found in $BACKUP_BUCKET/$PREFIX"
		exit 1
	fi
	file="/tmp/restore.dump"
	s3 cp --only-show-errors "s3://$BACKUP_BUCKET/$PREFIX/$name" "$file"
	# --clean drops objects before recreating them, so this also works on a
	# database that migrations have already initialized.
	pg_restore --clean --if-exists --no-owner --exit-on-error --dbname="$PGDATABASE" "$file"
	rm -f "$file"
	log "restored $PREFIX/$name"
}

# Backups run at a fixed hour, not on start: a freshly created server must not
# upload its empty database before the real data is restored.
run() {
	log "daily backups at ${HOUR}:00 UTC, keeping $KEEP"
	while true; do
		now=$(date -u +%s)
		next=$((now - now % 86400 + HOUR * 3600))
		[ "$next" -gt "$now" ] || next=$((next + 86400))
		sleep $((next - now))
		# A separate process keeps `set -e`, which is ignored inside `||`.
		"$0" backup || log "backup failed"
	done
}

case "${1:-run}" in
run) run ;;
backup) backup ;;
list) list ;;
restore) restore "${2:-}" ;;
*)
	echo "usage: backup.sh run|backup|list|restore [name]" >&2
	exit 2
	;;
esac
