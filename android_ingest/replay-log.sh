#/bin/bash
# This a script to replay the transaction log for android_ingest.

# This script is safe to run multiple times, i.e. data re-uploaded
# will be re-ingested by Perf just fine.

set -x -e

# Change the below gs: url to capture all the data you want to replay.
for FILENAME in $(gsutil ls "gs://skia-perf/android-master-ingest/tx_log/2018/06/27/**")
do
  curl -d "`gsutil cat $FILENAME`" -H "Content-Type: application/json" -X POST https://android-master-ingest.skia.org/upload
done

