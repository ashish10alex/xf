#!/usr/bin/env bash
# Loads the example projects' raw tables into BigQuery: the `raw` dataset of
# the project named by $1 (or $XF_GCP_PROJECT), in the EU, replacing what is
# there. Uses the bq CLI and your gcloud login. Load jobs are free, and the
# tables hold a few dozen rows.
set -euo pipefail
project=${1:-${XF_GCP_PROJECT:?usage: load.sh <gcp-project> (or set XF_GCP_PROJECT)}}
location=EU
here=$(cd "$(dirname "$0")" && pwd)

if ! bq --project_id="$project" show --dataset "$project:raw" >/dev/null 2>&1; then
    bq --project_id="$project" mk --dataset --location="$location" \
        --description "Raw tables for the xf example projects" "$project:raw"
fi

load() {
    bq --project_id="$project" --location="$location" load --replace \
        --source_format=CSV --skip_leading_rows=1 \
        "$project:raw.$1" "$here/raw/$1.csv" "$2"
}
load customers id:INT64,email:STRING,country_code:STRING,created_at:TIMESTAMP,updated_at:TIMESTAMP,_loaded_at:TIMESTAMP
load orders id:INT64,user_id:INT64,order_date:DATE,status:STRING
load payments id:INT64,order_id:INT64,payment_method:STRING,amount:INT64,status:STRING

bq --project_id="$project" ls "$project:raw"
