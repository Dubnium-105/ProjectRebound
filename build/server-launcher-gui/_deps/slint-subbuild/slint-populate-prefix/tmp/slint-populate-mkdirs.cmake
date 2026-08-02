# Distributed under the OSI-approved BSD 3-Clause License.  See accompanying
# file LICENSE.rst or https://cmake.org/licensing for details.

cmake_minimum_required(VERSION ${CMAKE_VERSION}) # this file comes with cmake

# If CMAKE_DISABLE_SOURCE_CHANGES is set to true and the source directory is an
# existing directory in our source tree, calling file(MAKE_DIRECTORY) on it
# would cause a fatal error, even though it would be a no-op.
if(NOT EXISTS "C:/wksp/ProjectRebound/build/server-launcher-gui/_deps/slint-src")
  file(MAKE_DIRECTORY "C:/wksp/ProjectRebound/build/server-launcher-gui/_deps/slint-src")
endif()
file(MAKE_DIRECTORY
  "C:/wksp/ProjectRebound/build/server-launcher-gui/_deps/slint-build"
  "C:/wksp/ProjectRebound/build/server-launcher-gui/_deps/slint-subbuild/slint-populate-prefix"
  "C:/wksp/ProjectRebound/build/server-launcher-gui/_deps/slint-subbuild/slint-populate-prefix/tmp"
  "C:/wksp/ProjectRebound/build/server-launcher-gui/_deps/slint-subbuild/slint-populate-prefix/src/slint-populate-stamp"
  "C:/wksp/ProjectRebound/build/server-launcher-gui/_deps/slint-subbuild/slint-populate-prefix/src"
  "C:/wksp/ProjectRebound/build/server-launcher-gui/_deps/slint-subbuild/slint-populate-prefix/src/slint-populate-stamp"
)

set(configSubDirs Debug)
foreach(subDir IN LISTS configSubDirs)
    file(MAKE_DIRECTORY "C:/wksp/ProjectRebound/build/server-launcher-gui/_deps/slint-subbuild/slint-populate-prefix/src/slint-populate-stamp/${subDir}")
endforeach()
if(cfgdir)
  file(MAKE_DIRECTORY "C:/wksp/ProjectRebound/build/server-launcher-gui/_deps/slint-subbuild/slint-populate-prefix/src/slint-populate-stamp${cfgdir}") # cfgdir has leading slash
endif()
