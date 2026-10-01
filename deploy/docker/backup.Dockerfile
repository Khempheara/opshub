# syntax=docker/dockerfile:1.7
# Backup image: pg_dump/pg_restore 17 (matching the server), age (encryption) and rclone
# (optional off-site copy). Runs as the unprivileged postgres user (uid 70).
FROM postgres:17-alpine AS runtime
# gosu (the server entrypoint's privilege drop) isn't used here: this image already runs
# unprivileged, and removing it drops its outdated Go standard library from the image.
RUN apk upgrade --no-cache && apk add --no-cache age rclone \
 && rm -f /usr/local/bin/gosu \
 && mkdir -p /backups && chown 70:70 /backups
COPY --chmod=755 deploy/backup/backup.sh /usr/local/bin/opshub-backup
COPY --chmod=755 deploy/backup/restore.sh /usr/local/bin/opshub-restore
COPY --chmod=755 deploy/backup/schedule.sh /usr/local/bin/opshub-backup-schedule
USER 70:70
VOLUME /backups
ENTRYPOINT []
CMD ["/usr/local/bin/opshub-backup-schedule"]
