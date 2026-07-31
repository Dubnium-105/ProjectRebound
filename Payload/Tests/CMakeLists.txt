cmake_minimum_required(VERSION 3.20)
project(ProjectReboundCommandProtocolTests LANGUAGES CXX)

enable_testing()

function(enable_strict_warnings target)
    if(MSVC)
        target_compile_options(${target} PRIVATE /W4 /WX /utf-8)
    else()
        target_compile_options(${target} PRIVATE -Wall -Wextra -Wpedantic -Werror)
    endif()
endfunction()

add_executable(command_protocol_tests
    CommandProtocolTests.cpp
    ../Communication/CommandProtocol.cpp
)

target_compile_features(command_protocol_tests PRIVATE cxx_std_20)
enable_strict_warnings(command_protocol_tests)

add_test(NAME command_protocol_tests COMMAND command_protocol_tests)

if(WIN32)
    add_executable(command_framework_tests
        CommandFrameworkTests.cpp
        ../Communication/CommandFramework.cpp
        ../Communication/CommandProtocol.cpp
    )

    target_compile_features(command_framework_tests PRIVATE cxx_std_20)
    enable_strict_warnings(command_framework_tests)
    target_link_libraries(command_framework_tests PRIVATE Advapi32)

    add_test(NAME command_framework_tests COMMAND command_framework_tests)
endif()
