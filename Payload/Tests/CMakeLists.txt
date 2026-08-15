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

add_executable(unreal_container_tests
    UnrealContainerTests.cpp
)

target_compile_features(unreal_container_tests PRIVATE cxx_std_20)
enable_strict_warnings(unreal_container_tests)
if(MSVC)
    # Generated Unreal container declarations intentionally use anonymous
    # unions and SDK-width integer constants. Keep project code strict while
    # suppressing those two warnings for this focused regression harness.
    target_compile_options(unreal_container_tests PRIVATE /wd4201 /wd4245)
else()
    target_compile_options(unreal_container_tests PRIVATE -Wno-pedantic)
endif()

add_test(NAME unreal_container_tests COMMAND unreal_container_tests)

add_executable(loadout_state_policy_tests
    LoadoutStatePolicyTests.cpp
)

target_compile_features(loadout_state_policy_tests PRIVATE cxx_std_20)
enable_strict_warnings(loadout_state_policy_tests)

add_test(NAME loadout_state_policy_tests COMMAND loadout_state_policy_tests)

add_executable(archive_completion_policy_tests
    ArchiveCompletionPolicyTests.cpp
)

target_compile_features(archive_completion_policy_tests PRIVATE cxx_std_20)
enable_strict_warnings(archive_completion_policy_tests)

add_test(NAME archive_completion_policy_tests COMMAND archive_completion_policy_tests)

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
