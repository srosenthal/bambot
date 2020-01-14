Bambot [![Build Status](https://img.shields.io/circleci/build/github/srosenthal/bambot)](https://circleci.com/gh/srosenthal/bambot) [![License](https://img.shields.io/github/license/srosenthal/bambot)](https://github.com/srosenthal/bambot/blob/master/LICENSE)
=========

# The Problem
Atlassian Bamboo is an automated build system like TeamCity, Jenkins, Travis, or CircleCI. It doesn't have the greatest user interface. When a build fails because of a test failure (say, JUnit), it does a good job of indicating that in the UI. But if the build fails for less common reasons (compilation error, missing code coverage, etc.), you have to resort to opening the build logs, and searching for the failure. Seeq's build logs are long and unfriendly to new developers, so this isn't much fun at all!


# The Solution: Bambot
Bambot is a side project I developed at [Seeq](https://seeq.com) and launched in August 2019. It should be run on a schedule with a tool like cron or SystemD. Bambot scans the Atom feed of recent build failures, looks for known failure patterns, and posts a comment with an excerpt of the logs.

Here's what a Bambot comment looks like in Bamboo
![Example of Bambot's comment](https://github.com/srosenthal/bambot/blob/master/bambot-comment.png "Example of Bambot's comment")
