#Use Vulcanize 1.0
VULCANIZE1=true
# The list of files we want to go into core.js, which is concat'd and
# minified. These files should be either present in the project, brought
# into third_party/bower_compoents via bower, or in node_modules.
CORE_SOURCE_FILES = third_party/bower_components/webcomponentsjs/webcomponents.min.js \
          ../res/js/common.js \
          res/js/fuzzer.js

BOWER_DIR=third_party/bower_components

default: all

all: clean_webtools core_js elements_html skiaversion frontend backend

fe_release: all
	./build_fe_release "`git log -n1 --format=%s`"

deprecated_be_release: all
	CONFIG=1 ./deprecated_build_be_release "`git log -n1 --format=%s`"
	CONFIG=2 ./deprecated_build_be_release "`git log -n1 --format=%s`"
	CONFIG=3 ./deprecated_build_be_release "`git log -n1 --format=%s`"

be_release: backend
	./docker_build_be_release

release: all fe_release be_release
	echo

test: testgo testjs
	echo

testgo:
	go test ./go/...

frontend:
	go install -v ./go/fuzzer-fe

backend:
	go install -v ./go/fuzzer-be

include ../webtools/webtools.mk
include ../go/skiaversion/skiaversion.mk
