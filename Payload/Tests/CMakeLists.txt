cmake_minimum_required(VERSION 3.20)
project(ProjectReboundCommandProtocolTests LANGUAGES CXX)

enable_testing()

add_executable(command_protocol_tests
    CommandProtocolTests.cpp
    ../Communication/CommandProtocol.cpp
)

target_compile_features(command_protocol_tests PRIVATE cxx_std_20)

if(MSVC)
    target_compile_options(command_protocol_tests PRIVATE /W4 /WX /utf-8)
else()
    target_compile_options(command_protocol_tests PRIVATE -Wall -Wextra -Wpedantic -Werror)
endif()

add_test(NAME command_protocol_tests COMMAND command_protocol_tests)
